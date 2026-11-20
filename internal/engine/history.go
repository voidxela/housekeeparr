package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/store"
)

// HistoryFeedReader is a manager whose whole history can be read in pages,
// newest first.
type HistoryFeedReader interface {
	HistoryFeed(ctx context.Context, page, pageSize int) (arr.HistoryPage, error)
}

// Manager history is read incrementally for the release-source scorecard:
// at most historyPages pages of historyPageSize records per read, every
// historyInterval, back to historyHorizon on the first reads.
const (
	historyInterval = 30 * time.Minute
	historyPageSize = 250
	historyPages    = 8
	historyHorizon  = 30 * 24 * time.Hour
)

// historyRead is one bounded read of a manager's history.
type historyRead struct {
	Records []store.HistoryEvent
	Cursor  store.HistoryCursor
	Pages   int
}

// readHistoryFeed reads what is new since cur (nil before the first read),
// then continues the backfill toward the horizon with the remaining page
// budget. Records are deduplicated by the store; the cursor advances only
// over records actually read, so a read interrupted at any point resumes
// without losing or double-counting anything. If more records arrived
// since the last read than one read covers, the records in between may be
// missing: coverage restarts at the oldest record read (GapAt) and the
// backfill covers the gap again.
func readHistoryFeed(ctx context.Context, managerID string, r HistoryFeedReader, cur *store.HistoryCursor, now time.Time) (historyRead, error) {
	out := historyRead{}
	next := store.HistoryCursor{ManagerID: managerID, UpdatedAt: now}
	if cur != nil {
		next = *cur
		next.UpdatedAt = now
	}
	var oldest *time.Time
	add := func(rec arr.HistoryRecord) {
		out.Records = append(out.Records, toStored(managerID, rec))
		if oldest == nil || rec.Date.Before(*oldest) {
			d := rec.Date
			oldest = &d
		}
	}
	// Head: everything newer than the cursor.
	var maxID int64
	total, closed, end := 0, false, false
	page := 1
	for ; out.Pages < historyPages; page++ {
		pg, err := r.HistoryFeed(ctx, page, historyPageSize)
		if err != nil {
			return out, err
		}
		out.Pages++
		if page == 1 {
			total = pg.Total
		}
		for _, rec := range pg.Records {
			maxID = max(maxID, rec.ID)
			if cur != nil && rec.ID <= cur.NewestID {
				closed = true
				continue
			}
			add(rec)
		}
		if len(pg.Records) < historyPageSize {
			end = true
		}
		if closed || end {
			break
		}
	}
	read := (page - 1) * historyPageSize
	if closed || end {
		read = page * historyPageSize
	}
	switch {
	case cur == nil:
		// The first read starts the backfill.
		next.NewestID, next.CoveredSince = maxID, oldest
		next.BackfillIndex, next.BackfillTotal = read, total
		next.Complete = end || (oldest != nil && now.Sub(*oldest) >= historyHorizon)
	case closed:
		next.NewestID = max(maxID, cur.NewestID)
	default:
		// Not caught up within one read: what lies between may be missing.
		next.NewestID = maxID
		next.GapAt, next.CoveredSince = oldest, oldest
		next.BackfillIndex, next.BackfillTotal, next.Complete = read, total, end
	}
	if cur == nil && next.Complete {
		return finish(out, next, oldest), nil
	}
	// Backfill toward the horizon. Records added since the position was
	// recorded shift it; one page of overlap absorbs rounding.
	if !next.Complete && out.Pages < historyPages {
		idx := max(next.BackfillIndex+(total-next.BackfillTotal)-historyPageSize, 0)
		page := idx/historyPageSize + 1
		for ; out.Pages < historyPages; page++ {
			pg, err := r.HistoryFeed(ctx, page, historyPageSize)
			if err != nil {
				return out, err
			}
			out.Pages++
			for _, rec := range pg.Records {
				add(rec)
			}
			if len(pg.Records) < historyPageSize || (oldest != nil && now.Sub(*oldest) >= historyHorizon) {
				next.Complete = true
				page++
				break
			}
		}
		next.BackfillIndex, next.BackfillTotal = (page-1)*historyPageSize, total
		if oldest != nil && (next.CoveredSince == nil || oldest.Before(*next.CoveredSince)) {
			next.CoveredSince = oldest
		}
	}
	return finish(out, next, oldest), nil
}

func finish(out historyRead, next store.HistoryCursor, oldest *time.Time) historyRead {
	if next.Complete && next.GapAt != nil && next.CoveredSince != nil && next.CoveredSince.Before(*next.GapAt) {
		// The backfill read past the gap again: coverage is contiguous.
		next.GapAt = nil
	}
	out.Cursor = next
	return out
}

func toStored(managerID string, r arr.HistoryRecord) store.HistoryEvent {
	e := store.HistoryEvent{ManagerID: managerID, EventID: r.ID, EventType: r.EventType, At: r.Date, DownloadID: r.DownloadID,
		SourceTitle: r.SourceTitle, Indexer: r.Indexer, ReleaseGroup: r.ReleaseGroup, ReleaseSource: r.ReleaseSource}
	switch {
	case r.EpisodeID > 0:
		e.ContentKey = fmt.Sprintf("episode:%d", r.EpisodeID)
	case r.MovieID > 0:
		e.ContentKey = fmt.Sprintf("movie:%d", r.MovieID)
	}
	return e
}

// readHistory refreshes, at most every historyInterval, the history of
// managers read successfully this cycle. It is observation only and runs
// whatever automation is enabled. A failure is logged and changes nothing.
func (e *Engine) readHistory(ctx context.Context, results []*fetched) {
	ok := map[string]bool{}
	for _, f := range results {
		if f.queue != nil && f.err == nil {
			ok[f.id] = true
		}
	}
	for _, m := range e.conns.Managers {
		hr, can := m.(HistoryFeedReader)
		if !can || !ok[m.ID()] {
			continue
		}
		now := e.clock.Now()
		e.mu.Lock()
		last, seen := e.historyRead[m.ID()]
		e.mu.Unlock()
		if age := now.Sub(last); seen && age >= 0 && age < historyInterval {
			continue
		}
		cur, err := e.st.HistoryCursor(ctx, m.ID())
		if err != nil {
			e.log.Error("history cursor not read", "manager", m.ID(), "err", err)
			continue
		}
		rd, err := readHistoryFeed(ctx, m.ID(), hr, cur, now)
		if err != nil {
			e.log.Warn("manager history not read", "manager", m.ID(), "class", adapter.ClassOf(err), "error", errorText(err))
			continue
		}
		var added int
		if err := e.st.Write(ctx, func(tx *store.Tx) error {
			var err error
			if added, err = tx.SaveHistory(rd.Records); err != nil {
				return err
			}
			return tx.SetHistoryCursor(rd.Cursor)
		}); err != nil {
			e.log.Error("manager history not saved", "manager", m.ID(), "err", err)
			continue
		}
		e.mu.Lock()
		if e.historyRead == nil {
			e.historyRead = map[string]time.Time{}
		}
		e.historyRead[m.ID()] = now
		e.mu.Unlock()
		e.log.Debug("manager history read", "manager", m.ID(), "pages", rd.Pages, "new_records", added, "complete", rd.Cursor.Complete)
	}
}
