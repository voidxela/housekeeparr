package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// SourceRecord is a stored source and how many consecutive reads found it
// not disabled.
type SourceRecord struct {
	domain.Source
	ManagerID   string    `json:"manager_id"`
	OKStreak    int       `json:"ok_streak"`
	CollectedAt time.Time `json:"collected_at"`
}

// Unavailable reports whether the manager had the source disabled at t.
func (r SourceRecord) Unavailable(t time.Time) bool {
	return r.DisabledUntil != nil && r.DisabledUntil.After(t)
}

// SaveSources replaces a manager's stored sources with a fresh read,
// carrying each source's streak of reads in which it was not disabled.
func (t *Tx) SaveSources(s domain.SourceSnapshot) error {
	prev, err := t.sources(s.ManagerID)
	if err != nil {
		return err
	}
	if _, err := t.exec(`DELETE FROM source_status WHERE manager_id=?`, s.ManagerID); err != nil {
		return err
	}
	for _, src := range s.Sources {
		streak := 0
		if src.DisabledUntil == nil || !src.DisabledUntil.After(s.CollectedAt) {
			streak = 1
			if p, ok := prev[src.ID]; ok {
				streak = p.OKStreak + 1
			}
		}
		auto := 0
		if src.AutomaticSearch {
			auto = 1
		}
		if _, err := t.exec(`INSERT INTO source_status(manager_id, source_id, name, protocol, automatic, disabled_until, last_failure, ok_streak, collected_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, s.ManagerID, src.ID, src.Name, src.Protocol, auto, ptrTime(src.DisabledUntil), ptrTime(src.LastFailure),
			streak, ms(s.CollectedAt)); err != nil {
			return err
		}
	}
	return nil
}

func scanSources(rows *sql.Rows) ([]SourceRecord, error) {
	defer rows.Close()
	var out []SourceRecord
	for rows.Next() {
		var r SourceRecord
		var auto int
		var dis, fail sql.NullInt64
		var at int64
		if err := rows.Scan(&r.ManagerID, &r.ID, &r.Name, &r.Protocol, &auto, &dis, &fail, &r.OKStreak, &at); err != nil {
			return nil, err
		}
		r.AutomaticSearch, r.DisabledUntil, r.LastFailure, r.CollectedAt = auto == 1, ptrMS(dis), ptrMS(fail), fromMS(at)
		out = append(out, r)
	}
	return out, rows.Err()
}

const sourceCols = `manager_id, source_id, name, protocol, automatic, disabled_until, last_failure, ok_streak, collected_at`

func (t *Tx) sources(managerID string) (map[int64]SourceRecord, error) {
	rows, err := t.query("SELECT "+sourceCols+" FROM source_status WHERE manager_id=?", managerID)
	if err != nil {
		return nil, err
	}
	list, err := scanSources(rows)
	out := map[int64]SourceRecord{}
	for _, r := range list {
		out[r.ID] = r
	}
	return out, err
}

// Sources lists stored sources, for one manager or all.
func (s *Store) Sources(ctx context.Context, managerID string) ([]SourceRecord, error) {
	q, args := "SELECT "+sourceCols+" FROM source_status", []any{}
	if managerID != "" {
		q += " WHERE manager_id=?"
		args = append(args, managerID)
	}
	rows, err := s.r.QueryContext(ctx, q+" ORDER BY manager_id, name", args...)
	if err != nil {
		return nil, err
	}
	return scanSources(rows)
}

// SourcesOf lists a manager's stored sources inside a transaction.
func (t *Tx) SourcesOf(managerID string) ([]SourceRecord, error) {
	rows, err := t.query("SELECT "+sourceCols+" FROM source_status WHERE manager_id=? ORDER BY name", managerID)
	if err != nil {
		return nil, err
	}
	return scanSources(rows)
}
