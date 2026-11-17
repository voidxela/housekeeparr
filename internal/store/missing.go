package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// Missing-content search states (SPEC §9.1). They describe Housekeeparr's
// own searches; a search accepted or completed says nothing about a grab.
const (
	MissingNotSearched = "not_searched"
	MissingSearching   = "searching" // a search action is in progress
	MissingSearched    = "searched"  // the manager accepted the search; see note
	MissingBackoff     = "backoff"   // searched without a grab; waiting
	MissingUncertain   = "uncertain" // the search request's outcome is unknown
	MissingGone        = "gone"      // no longer missing (or no longer listed)
)

// MissingRecord is one missing episode or movie and its search state.
type MissingRecord struct {
	ManagerID    string     `json:"manager_id"`
	ContentKey   string     `json:"content_key"`
	SeriesID     int64      `json:"series_id,omitempty"`
	Title        string     `json:"title"`
	AiredAt      *time.Time `json:"aired_at,omitempty"`
	Available    *bool      `json:"available,omitempty"`
	FirstSeen    time.Time  `json:"first_seen"`
	LastSeen     time.Time  `json:"last_seen"`
	Present      bool       `json:"present"`
	GoneAt       *time.Time `json:"gone_at,omitempty"`
	Searches     int        `json:"searches"`
	LastSearchAt *time.Time `json:"last_search_at,omitempty"`
	LastIntentID *int64     `json:"last_intent_id,omitempty"`
	NextSearchAt *time.Time `json:"next_search_at,omitempty"`
	State        string     `json:"state"`
	Note         string     `json:"note,omitempty"`
}

// MissingKey is the content key of a missing item.
func MissingKey(it domain.MissingItem) string {
	return fmt.Sprintf("%s:%d", it.ContentKind, it.ContentID)
}

// SaveMissingSnapshot upserts a manager's missing items. Only a complete
// snapshot marks unlisted items gone; it returns how many were.
func (t *Tx) SaveMissingSnapshot(s domain.MissingSnapshot, now time.Time) (int, error) {
	for _, it := range s.Items {
		var avail sql.NullInt64
		if v, ok := it.Available.Get(); ok {
			avail = sql.NullInt64{Valid: true}
			if v {
				avail.Int64 = 1
			}
		}
		var aired sql.NullInt64
		if it.AiredAt != nil {
			aired = sql.NullInt64{Int64: ms(*it.AiredAt), Valid: true}
		}
		var series sql.NullInt64
		if it.SeriesID > 0 {
			series = sql.NullInt64{Int64: it.SeriesID, Valid: true}
		}
		if _, err := t.exec(`INSERT INTO missing_content(manager_id, content_key, series_id, title, aired_at, available, first_seen, last_seen)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(manager_id, content_key) DO UPDATE SET series_id=excluded.series_id, title=excluded.title, aired_at=excluded.aired_at,
				available=excluded.available, last_seen=excluded.last_seen, present=1, gone_at=NULL,
				state=CASE WHEN state='gone' THEN 'not_searched' ELSE state END`,
			s.ManagerID, MissingKey(it), series, it.Title, aired, avail, ms(now), ms(now)); err != nil {
			return 0, err
		}
	}
	if !s.Complete {
		return 0, nil
	}
	res, err := t.exec(`UPDATE missing_content SET present=0, gone_at=?, state='gone' WHERE manager_id=? AND present=1 AND last_seen < ?`,
		ms(now), s.ManagerID, ms(now))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

const missingCols = `manager_id, content_key, series_id, title, aired_at, available, first_seen, last_seen, present, gone_at, searches,
	last_search_at, last_intent_id, next_search_at, state, note`

func scanMissing(rows *sql.Rows) ([]MissingRecord, error) {
	defer rows.Close()
	var out []MissingRecord
	for rows.Next() {
		var r MissingRecord
		var series, aired, avail, gone, lastSearch, intent, next sql.NullInt64
		var first, last int64
		if err := rows.Scan(&r.ManagerID, &r.ContentKey, &series, &r.Title, &aired, &avail, &first, &last, &r.Present, &gone, &r.Searches,
			&lastSearch, &intent, &next, &r.State, &r.Note); err != nil {
			return nil, err
		}
		r.SeriesID = series.Int64
		r.AiredAt, r.GoneAt, r.LastSearchAt, r.NextSearchAt = ptrMS(aired), ptrMS(gone), ptrMS(lastSearch), ptrMS(next)
		r.FirstSeen, r.LastSeen = fromMS(first), fromMS(last)
		if avail.Valid {
			b := avail.Int64 == 1
			r.Available = &b
		}
		if intent.Valid {
			r.LastIntentID = &intent.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MissingFilter selects missing content.
type MissingFilter struct {
	ManagerID   string
	PresentOnly bool
	Limit       int
}

// MissingContent lists missing content, never-searched and oldest-first.
func (s *Store) MissingContent(ctx context.Context, f MissingFilter) ([]MissingRecord, error) {
	q := "SELECT " + missingCols + " FROM missing_content WHERE 1=1"
	var args []any
	if f.ManagerID != "" {
		q += " AND manager_id=?"
		args = append(args, f.ManagerID)
	}
	if f.PresentOnly {
		q += " AND present=1"
	}
	if f.Limit <= 0 {
		f.Limit = 1000
	}
	q += " ORDER BY searches ASC, COALESCE(last_search_at, 0) ASC, COALESCE(aired_at, 0) DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanMissing(rows)
}

// MissingRecord returns one item, or nil.
func (s *Store) MissingRecord(ctx context.Context, managerID, key string) (*MissingRecord, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT "+missingCols+" FROM missing_content WHERE manager_id=? AND content_key=?", managerID, key)
	if err != nil {
		return nil, err
	}
	list, err := scanMissing(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// SetMissingSearch records Housekeeparr's search state for an item.
func (t *Tx) SetMissingSearch(managerID, key, state, note string, intentID *int64, searchedAt, next *time.Time, countSearch bool) error {
	inc := 0
	if countSearch {
		inc = 1
	}
	_, err := t.exec(`UPDATE missing_content SET state=?, note=?, last_intent_id=COALESCE(?, last_intent_id),
		last_search_at=COALESCE(?, last_search_at), next_search_at=?, searches=searches+? WHERE manager_id=? AND content_key=?`,
		state, note, nullInt(intentID), ptrTime(searchedAt), ptrTime(next), inc, managerID, key)
	return err
}

// LastMissingSearch returns when Housekeeparr last sent a proactive search.
func (s *Store) LastMissingSearch(ctx context.Context) (*time.Time, error) {
	var v sql.NullInt64
	if err := s.r.QueryRowContext(ctx, "SELECT max(last_search_at) FROM missing_content").Scan(&v); err != nil {
		return nil, err
	}
	return ptrMS(v), nil
}
