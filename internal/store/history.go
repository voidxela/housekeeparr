package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HistoryEvent is one stored manager history record.
type HistoryEvent struct {
	ManagerID     string    `json:"manager_id"`
	EventID       int64     `json:"event_id"`
	EventType     string    `json:"event_type"`
	At            time.Time `json:"at"`
	DownloadID    string    `json:"download_id,omitempty"`
	ContentKey    string    `json:"content_key,omitempty"`
	SourceTitle   string    `json:"source_title,omitempty"`
	Indexer       string    `json:"indexer,omitempty"`
	ReleaseGroup  string    `json:"release_group,omitempty"`
	ReleaseSource string    `json:"release_source,omitempty"`
}

// SaveHistory stores records idempotently (a record read twice, from
// overlapping pages or after a restart, is kept once). It returns how many
// were new.
func (t *Tx) SaveHistory(evs []HistoryEvent) (int, error) {
	n := 0
	for _, e := range evs {
		res, err := t.exec(`INSERT INTO manager_history(manager_id, event_id, event_type, at, download_id, content_key, source_title, indexer,
			release_group, release_source) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(manager_id, event_id) DO NOTHING`,
			e.ManagerID, e.EventID, e.EventType, ms(e.At), nullStr(e.DownloadID), nullStr(e.ContentKey), nullStr(e.SourceTitle),
			nullStr(e.Indexer), nullStr(e.ReleaseGroup), nullStr(e.ReleaseSource))
		if err != nil {
			return n, err
		}
		if k, _ := res.RowsAffected(); k > 0 {
			n++
		}
	}
	return n, nil
}

// HistoryCursor is how far a manager's history has been read.
type HistoryCursor struct {
	ManagerID string `json:"manager_id"`
	// NewestID: every record from CoveredSince up to it is stored.
	NewestID     int64      `json:"newest_id"`
	CoveredSince *time.Time `json:"covered_since,omitempty"`
	// BackfillIndex/BackfillTotal continue the read toward the horizon.
	BackfillIndex int  `json:"-"`
	BackfillTotal int  `json:"-"`
	Complete      bool `json:"complete"`
	// GapAt is set when more records arrived between reads than one read
	// covers: records before it may be missing, so coverage restarts there.
	GapAt     *time.Time `json:"gap_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// HistoryCursor returns a manager's cursor, nil before its first read.
func (s *Store) HistoryCursor(ctx context.Context, managerID string) (*HistoryCursor, error) {
	c := HistoryCursor{ManagerID: managerID}
	var since, gap sql.NullInt64
	var updated int64
	err := s.r.QueryRowContext(ctx, `SELECT newest_id, covered_since, backfill_index, backfill_total, complete, gap_at, updated_at
		FROM history_cursors WHERE manager_id=?`, managerID).Scan(&c.NewestID, &since, &c.BackfillIndex, &c.BackfillTotal, &c.Complete, &gap, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.CoveredSince, c.GapAt, c.UpdatedAt = ptrMS(since), ptrMS(gap), fromMS(updated)
	return &c, nil
}

// HistoryCursors returns every manager's cursor.
func (s *Store) HistoryCursors(ctx context.Context) (map[string]HistoryCursor, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT manager_id FROM history_cursors")
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := map[string]HistoryCursor{}
	for _, id := range ids {
		c, err := s.HistoryCursor(ctx, id)
		if err != nil {
			return nil, err
		}
		if c != nil {
			out[id] = *c
		}
	}
	return out, nil
}

// SetHistoryCursor records a manager's cursor.
func (t *Tx) SetHistoryCursor(c HistoryCursor) error {
	_, err := t.exec(`INSERT INTO history_cursors(manager_id, newest_id, covered_since, backfill_index, backfill_total, complete, gap_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(manager_id) DO UPDATE SET newest_id=excluded.newest_id, covered_since=excluded.covered_since,
		backfill_index=excluded.backfill_index, backfill_total=excluded.backfill_total, complete=excluded.complete, gap_at=excluded.gap_at,
		updated_at=excluded.updated_at`,
		c.ManagerID, c.NewestID, nullMS(c.CoveredSince), c.BackfillIndex, c.BackfillTotal, c.Complete, nullMS(c.GapAt), ms(c.UpdatedAt))
	return err
}

// HistoryForGrabsSince returns every stored record of each download that
// has a grab at or after since, per manager, oldest first.
func (s *Store) HistoryForGrabsSince(ctx context.Context, since time.Time) ([]HistoryEvent, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT h.manager_id, h.event_id, h.event_type, h.at, COALESCE(h.download_id, ''), COALESCE(h.content_key, ''),
		COALESCE(h.source_title, ''), COALESCE(h.indexer, ''), COALESCE(h.release_group, ''), COALESCE(h.release_source, '')
		FROM manager_history h WHERE h.download_id IS NOT NULL AND EXISTS (SELECT 1 FROM manager_history g WHERE g.manager_id = h.manager_id
			AND g.download_id = h.download_id AND g.event_type = 'grabbed' AND g.at >= ?)
		ORDER BY h.manager_id, h.download_id, h.at, h.event_id`, ms(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEvent
	for rows.Next() {
		var e HistoryEvent
		var at int64
		if err := rows.Scan(&e.ManagerID, &e.EventID, &e.EventType, &at, &e.DownloadID, &e.ContentKey, &e.SourceTitle, &e.Indexer,
			&e.ReleaseGroup, &e.ReleaseSource); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// HistoryForDownloads returns the stored records of the given downloads of
// one manager (for example the replacements recovery outcomes recorded).
func (s *Store) HistoryForDownloads(ctx context.Context, managerID string, downloadIDs []string) ([]HistoryEvent, error) {
	var out []HistoryEvent
	for _, d := range downloadIDs {
		rows, err := s.r.QueryContext(ctx, `SELECT event_id, event_type, at, COALESCE(content_key, '') FROM manager_history
			WHERE manager_id=? AND download_id=? ORDER BY at, event_id`, managerID, d)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			e := HistoryEvent{ManagerID: managerID, DownloadID: d}
			var at int64
			if err := rows.Scan(&e.EventID, &e.EventType, &at, &e.ContentKey); err != nil {
				rows.Close()
				return nil, err
			}
			e.At = fromMS(at)
			out = append(out, e)
		}
		rows.Close()
	}
	return out, nil
}
