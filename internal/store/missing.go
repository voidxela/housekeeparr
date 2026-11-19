package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// Missing-content search states (SPEC §9.1). They describe Housekeeparr's
// own searches; a search accepted or completed says nothing about a grab.
const (
	MissingNotSearched = "not_searched"
	MissingSearching   = "searching"      // a search action is in progress
	MissingSearched    = "searched"       // the manager accepted the search; see note
	MissingBackoff     = "backoff"        // searched without a grab; waiting
	MissingUncertain   = "uncertain"      // the search request's outcome is unknown
	MissingGone        = "gone"           // no longer missing (or no longer listed)
	MissingGrabbed     = "grabbed"        // the search completed reporting a release downloaded
	MissingNoResult    = "no_result"      // the search completed reporting nothing downloaded
	MissingSearchFail  = "search_failed"  // the search command failed or was aborted
	MissingEndUnknown  = "end_unverified" // the search's end, or its result, was not observed
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
	// Origin is "missing" or "pack" (a partial pack's follow-up, from the
	// action OriginIntent).
	Origin       string `json:"origin"`
	OriginIntent *int64 `json:"origin_intent,omitempty"`
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
	res, err := t.exec(`UPDATE missing_content SET present=0, gone_at=?, state=CASE WHEN state='searched' THEN state ELSE 'gone' END WHERE manager_id=? AND present=1 AND last_seen < ?`,
		ms(now), s.ManagerID, ms(now))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// MissingCursor returns the page the next windowed read of a manager's
// missing list starts at (1 when none is recorded).
func (s *Store) MissingCursor(ctx context.Context, managerID string) (int, error) {
	var v string
	err := s.r.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", "missing_page:"+managerID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 1 {
		return n, nil
	}
	return 1, nil
}

// SetMissingCursor records where the next windowed read starts.
func (t *Tx) SetMissingCursor(managerID string, page int) error {
	if page < 1 {
		page = 1
	}
	_, err := t.exec("INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "missing_page:"+managerID, strconv.Itoa(page))
	return err
}

const missingCols = `manager_id, content_key, series_id, title, aired_at, available, first_seen, last_seen, present, gone_at, searches,
	last_search_at, last_intent_id, next_search_at, state, note, origin, origin_intent`

func scanMissing(rows *sql.Rows) ([]MissingRecord, error) {
	defer rows.Close()
	var out []MissingRecord
	for rows.Next() {
		var r MissingRecord
		var series, aired, avail, gone, lastSearch, intent, next, originIntent sql.NullInt64
		var first, last int64
		if err := rows.Scan(&r.ManagerID, &r.ContentKey, &series, &r.Title, &aired, &avail, &first, &last, &r.Present, &gone, &r.Searches,
			&lastSearch, &intent, &next, &r.State, &r.Note, &r.Origin, &originIntent); err != nil {
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
		if originIntent.Valid {
			r.OriginIntent = &originIntent.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MissingFilter selects missing content.
type MissingFilter struct {
	ManagerID       string
	ManagerIDs      []string
	PresentOnly     bool
	State           string
	Limit           int
	LimitPerManager int
	FreshSince      time.Time
	SearchReadyAt   time.Time
}

// MissingContent lists missing content, never-searched and oldest-first.
func (s *Store) MissingContent(ctx context.Context, f MissingFilter) ([]MissingRecord, error) {
	from := " FROM missing_content WHERE 1=1"
	var args []any
	if f.ManagerID != "" {
		from += " AND manager_id=?"
		args = append(args, f.ManagerID)
	}
	if len(f.ManagerIDs) != 0 {
		from += " AND manager_id IN (" + strings.Repeat("?,", len(f.ManagerIDs)-1) + "?)"
		for _, id := range f.ManagerIDs {
			args = append(args, id)
		}
	}
	if !f.FreshSince.IsZero() {
		from += " AND last_seen>=?"
		args = append(args, ms(f.FreshSince))
	}
	if !f.SearchReadyAt.IsZero() {
		from += " AND available=1 AND last_seen<=? AND (next_search_at IS NULL OR next_search_at<=?)"
		args = append(args, ms(f.SearchReadyAt), ms(f.SearchReadyAt))
	}
	if f.PresentOnly {
		from += " AND present=1"
	}
	if f.State != "" {
		from += " AND state=?"
		args = append(args, f.State)
	}
	if f.Limit <= 0 {
		f.Limit = 1000
	}
	const priority = "(origin = 'pack') DESC, searches ASC, COALESCE(last_search_at, 0) ASC, COALESCE(aired_at, 0) DESC, manager_id, content_key"
	selection := "SELECT " + missingCols
	q := selection + from
	if f.LimitPerManager > 0 {
		// Incomplete snapshots retain unseen rows. Each manager gets its
		// own allowance even when another accumulates many such records.
		q = selection + " FROM (" + selection + ", ROW_NUMBER() OVER (PARTITION BY manager_id ORDER BY " + priority +
			") AS manager_rank" + from + ") WHERE manager_rank<=?"
		args = append(args, f.LimitPerManager)
	}
	q += " ORDER BY " + priority + " LIMIT ?"
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

// MissingRecord returns a missing item within the current transaction.
func (t *Tx) MissingRecord(managerID, key string) (*MissingRecord, error) {
	rows, err := t.tx.QueryContext(t.ctx, "SELECT "+missingCols+" FROM missing_content WHERE manager_id=? AND content_key=?", managerID, key)
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

// MarkPackFollowUp records that a cleared partial pack (action intentID)
// did not contain this episode: its next search is a targeted follow-up
// under the partial_pack permission. A row is created when the missing
// list has not been read yet (availability then stays unknown until it is).
func (t *Tx) MarkPackFollowUp(managerID, key, title string, intentID int64, now time.Time) error {
	_, err := t.exec(`INSERT INTO missing_content(manager_id, content_key, title, first_seen, last_seen, origin, origin_intent, next_search_at)
		VALUES(?, ?, ?, ?, ?, 'pack', ?, NULL)
		ON CONFLICT(manager_id, content_key) DO UPDATE SET origin='pack', origin_intent=excluded.origin_intent, next_search_at=NULL,
			state=CASE WHEN state IN ('gone') THEN 'not_searched' ELSE state END`, managerID, key, title, ms(now), ms(now), intentID)
	return err
}

// SetMissingOrigin changes why an item is searched.
func (t *Tx) SetMissingOrigin(managerID, key, origin string) error {
	_, err := t.exec(`UPDATE missing_content SET origin=? WHERE manager_id=? AND content_key=?`, origin, managerID, key)
	return err
}
