package engine

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/store"
)

// scriptFeed is a manager history that can grow between page reads and
// fail after a number of pages.
type scriptFeed struct {
	recs     []arr.HistoryRecord // ascending ID
	onPage   func(page int)
	failFrom int // 0: never
	pages    int
}

func (f *scriptFeed) add(n int, at time.Time, step time.Duration) {
	for i := 0; i < n; i++ {
		id := int64(len(f.recs) + 1)
		f.recs = append(f.recs, arr.HistoryRecord{ID: id, EventType: "grabbed", Date: at.Add(time.Duration(i) * step), DownloadID: "D" + string(rune('A'+id%26))})
	}
}

func (f *scriptFeed) HistoryFeed(_ context.Context, page, size int) (arr.HistoryPage, error) {
	f.pages++
	if f.failFrom > 0 && f.pages >= f.failFrom {
		return arr.HistoryPage{}, errors.New("connection reset")
	}
	if f.onPage != nil {
		f.onPage(page)
	}
	desc := append([]arr.HistoryRecord(nil), f.recs...)
	sort.Slice(desc, func(i, j int) bool { return desc[i].ID > desc[j].ID })
	out := arr.HistoryPage{Total: len(desc), Records: []arr.HistoryRecord{}}
	if s := (page - 1) * size; s < len(desc) {
		out.Records = desc[s:min(s+size, len(desc))]
	}
	return out, nil
}

// applyRead stores a read as readHistory does, returning the store's
// record IDs.
func applyRead(t *testing.T, st *store.Store, rd historyRead) {
	t.Helper()
	if err := st.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.SaveHistory(rd.Records); err != nil {
			return err
		}
		return tx.SetHistoryCursor(rd.Cursor)
	}); err != nil {
		t.Fatal(err)
	}
}

func storedIDs(t *testing.T, st *store.Store) map[int64]bool {
	t.Helper()
	evs, err := st.HistoryForGrabsSince(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]bool{}
	for _, e := range evs {
		if out[e.EventID] {
			t.Fatalf("record %d stored twice", e.EventID)
		}
		out[e.EventID] = true
	}
	return out
}

// Across a first read with backfill, records arriving between pages, a read
// failing part-way and a later burst larger than one read, every record is
// stored exactly once, coverage reports a gap until the backfill has read
// past it again, and the cursor never claims records it did not read.
func TestHistoryReadsAreIncrementalAndResumable(t *testing.T) {
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := t0.Add(60 * 24 * time.Hour)
	f := &scriptFeed{}
	// 2,600 records over the last 18 days: more than one read's 2,000.
	f.add(2600, now.Add(-2600*10*time.Minute), 10*time.Minute)
	cur := func() *store.HistoryCursor {
		c, err := st.HistoryCursor(ctx, "sonarr")
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	rd, err := readHistoryFeed(ctx, "sonarr", f, nil, now)
	if err != nil || rd.Pages != historyPages {
		t.Fatalf("first read: %d pages, %v", rd.Pages, err)
	}
	applyRead(t, st, rd)
	c := cur()
	if c.Complete || c.NewestID != 2600 || c.CoveredSince == nil {
		t.Fatalf("after the first read: %+v", c)
	}
	// Records arrive while the next read pages: nothing is skipped.
	f.onPage = func(page int) {
		if page == 1 {
			f.add(30, now, time.Second)
		}
	}
	rd, err = readHistoryFeed(ctx, "sonarr", f, c, now.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	applyRead(t, st, rd)
	f.onPage = nil
	ids := storedIDs(t, st)
	for id := int64(1); id <= 2630; id++ {
		// Records older than the 30-day horizon need not be read.
		if !ids[id] && f.recs[id-1].Date.After(now.Add(-historyHorizon)) {
			t.Fatalf("record %d (%s) not stored", id, f.recs[id-1].Date)
		}
	}
	if c = cur(); !c.Complete || c.NewestID != 2630 || c.GapAt != nil {
		t.Fatalf("after catching up: %+v", c)
	}
	// A read that fails part-way changes nothing; the next one resumes.
	f.add(10, now.Add(time.Hour), time.Second)
	f.failFrom, f.pages = 1, 0
	if _, err := readHistoryFeed(ctx, "sonarr", f, c, now.Add(time.Hour)); err == nil {
		t.Fatal("expected the read to fail")
	}
	f.failFrom = 0
	rd, err = readHistoryFeed(ctx, "sonarr", f, cur(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	applyRead(t, st, rd)
	if c = cur(); c.NewestID != 2640 || len(storedIDs(t, st)) != len(ids)+10 {
		t.Fatalf("after resuming: %+v, %d stored", c, len(storedIDs(t, st)))
	}
	// A burst larger than one read: coverage restarts at what was read.
	f.add(2100, now.Add(2*time.Hour), time.Second)
	rd, err = readHistoryFeed(ctx, "sonarr", f, cur(), now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	applyRead(t, st, rd)
	if c = cur(); c.GapAt == nil || c.Complete {
		t.Fatalf("after a burst: %+v (want a gap and an incomplete backfill)", c)
	}
	// Later reads backfill past the gap; coverage is contiguous again.
	for i := 0; i < 5 && (cur().GapAt != nil || !cur().Complete); i++ {
		rd, err = readHistoryFeed(ctx, "sonarr", f, cur(), now.Add(time.Duration(4+i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		applyRead(t, st, rd)
	}
	if c = cur(); c.GapAt != nil || !c.Complete || c.NewestID != 4740 {
		t.Fatalf("after backfilling the gap: %+v", c)
	}
	ids = storedIDs(t, st)
	for id := int64(2601); id <= 4740; id++ {
		if !ids[id] {
			t.Fatalf("record %d lost across the gap", id)
		}
	}
}
