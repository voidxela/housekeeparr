package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// CoverageSweep is a manager's sweep progress.
type CoverageSweep struct {
	ManagerID      string     `json:"manager_id"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	LastCompleteAt *time.Time `json:"last_complete_at,omitempty"`
}

// StartCoverageSweep records a complete group list and starts a sweep:
// groups no longer listed are removed with their gaps, new ones are added
// unread, titles are refreshed.
func (t *Tx) StartCoverageSweep(managerID string, groups []domain.CoverageGroup, now time.Time) error {
	if _, err := t.exec(`CREATE TEMP TABLE IF NOT EXISTS coverage_listed(group_id INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	if _, err := t.exec(`DELETE FROM coverage_listed`); err != nil {
		return err
	}
	for _, g := range groups {
		if _, err := t.exec(`INSERT OR IGNORE INTO coverage_listed(group_id) VALUES(?)`, g.ID); err != nil {
			return err
		}
		if _, err := t.exec(`INSERT INTO coverage_groups(manager_id, group_id, title, monitored) VALUES(?, ?, ?, ?)
			ON CONFLICT(manager_id, group_id) DO UPDATE SET title=excluded.title, monitored=excluded.monitored`, managerID, g.ID, g.Title, g.Monitored); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`DELETE FROM coverage_gaps WHERE manager_id=? AND group_id NOT IN (SELECT group_id FROM coverage_listed)`,
		`DELETE FROM coverage_groups WHERE manager_id=? AND group_id NOT IN (SELECT group_id FROM coverage_listed)`,
	} {
		if _, err := t.exec(q, managerID); err != nil {
			return err
		}
	}
	_, err := t.exec(`INSERT INTO coverage_sweeps(manager_id, started_at) VALUES(?, ?)
		ON CONFLICT(manager_id) DO UPDATE SET started_at=excluded.started_at, completed_at=NULL`, managerID, ms(now))
	return err
}

// CoverageSweepState returns a manager's sweep, or nil before the first.
func (s *Store) CoverageSweepState(ctx context.Context, managerID string) (*CoverageSweep, error) {
	var sw CoverageSweep
	var started int64
	var done, last sql.NullInt64
	err := s.r.QueryRowContext(ctx, `SELECT manager_id, started_at, completed_at, last_complete_at FROM coverage_sweeps WHERE manager_id=?`, managerID).
		Scan(&sw.ManagerID, &started, &done, &last)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sw.StartedAt, sw.CompletedAt, sw.LastCompleteAt = fromMS(started), ptrMS(done), ptrMS(last)
	return &sw, nil
}

// CoverageGroupsDue returns up to n groups not read since the sweep
// started, oldest read first.
func (s *Store) CoverageGroupsDue(ctx context.Context, managerID string, since time.Time, n int) ([]domain.CoverageGroup, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT group_id, title, monitored FROM coverage_groups WHERE manager_id=? AND (read_at IS NULL OR read_at < ?)
		ORDER BY read_at IS NOT NULL, read_at, group_id LIMIT ?`, managerID, ms(since), n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.CoverageGroup
	for rows.Next() {
		var g domain.CoverageGroup
		if err := rows.Scan(&g.ID, &g.Title, &g.Monitored); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SaveCoverageGroup records one group's read: its counts and its gaps
// (wanted items without a file). A gap keeps its first-seen time while it
// stays a gap.
func (t *Tx) SaveCoverageGroup(managerID string, groupID int64, items []domain.CoverageItem, now time.Time) error {
	wanted, have := 0, 0
	keep := map[string]bool{}
	for _, it := range items {
		if !it.Wanted {
			continue
		}
		wanted++
		if it.HasFile {
			have++
			continue
		}
		keep[it.Key] = true
		if _, err := t.exec(`INSERT INTO coverage_gaps(manager_id, content_key, group_id, label, aired_at, first_seen, last_seen) VALUES(?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(manager_id, content_key) DO UPDATE SET group_id=excluded.group_id, label=excluded.label, aired_at=excluded.aired_at, last_seen=excluded.last_seen`,
			managerID, it.Key, groupID, it.Label, ptrTime(it.AiredAt), ms(now), ms(now)); err != nil {
			return err
		}
	}
	rows, err := t.query(`SELECT content_key FROM coverage_gaps WHERE manager_id=? AND group_id=?`, managerID, groupID)
	if err != nil {
		return err
	}
	var drop []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		if !keep[k] {
			drop = append(drop, k)
		}
	}
	rows.Close()
	for _, k := range drop {
		if _, err := t.exec(`DELETE FROM coverage_gaps WHERE manager_id=? AND content_key=?`, managerID, k); err != nil {
			return err
		}
	}
	_, err = t.exec(`UPDATE coverage_groups SET wanted=?, have=?, read_at=? WHERE manager_id=? AND group_id=?`, wanted, have, ms(now), managerID, groupID)
	return err
}

// FinishCoverageSweepIfDone completes the sweep when every listed group has
// been read since it started, and records the day's coverage.
func (t *Tx) FinishCoverageSweepIfDone(managerID string, now time.Time) (bool, error) {
	var started int64
	if err := t.queryRow(`SELECT started_at FROM coverage_sweeps WHERE manager_id=? AND completed_at IS NULL`, managerID).Scan(&started); err == sql.ErrNoRows {
		return false, nil
	} else if err != nil {
		return false, err
	}
	var unread, groups, wanted, have int
	if err := t.queryRow(`SELECT COUNT(*) FILTER (WHERE read_at IS NULL OR read_at < ?), COUNT(*), COALESCE(SUM(wanted), 0), COALESCE(SUM(have), 0)
		FROM coverage_groups WHERE manager_id=?`, started, managerID).Scan(&unread, &groups, &wanted, &have); err != nil {
		return false, err
	}
	if unread > 0 {
		return false, nil
	}
	if _, err := t.exec(`UPDATE coverage_sweeps SET completed_at=?, last_complete_at=? WHERE manager_id=?`, ms(now), ms(now), managerID); err != nil {
		return false, err
	}
	_, err := t.exec(`INSERT INTO coverage_daily(manager_id, day, wanted, have, groups, at) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(manager_id, day) DO UPDATE SET wanted=excluded.wanted, have=excluded.have, groups=excluded.groups, at=excluded.at`,
		managerID, now.UTC().Format("2006-01-02"), wanted, have, groups, ms(now))
	return true, err
}

// CoverageTotals is a manager's coverage from the latest group reads.
type CoverageTotals struct {
	ManagerID  string     `json:"manager_id"`
	Wanted     int        `json:"wanted"`
	Have       int        `json:"have"`
	Groups     int        `json:"groups"`
	GroupsRead int        `json:"groups_read"`
	OldestRead *time.Time `json:"oldest_read,omitempty"`
	NewestRead *time.Time `json:"newest_read,omitempty"`
	Gaps       int        `json:"gaps"`
}

// CoverageTotals returns every manager's totals.
func (s *Store) CoverageTotals(ctx context.Context) ([]CoverageTotals, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT g.manager_id, COALESCE(SUM(g.wanted), 0), COALESCE(SUM(g.have), 0), COUNT(*), COUNT(g.read_at), MIN(g.read_at), MAX(g.read_at),
		(SELECT COUNT(*) FROM coverage_gaps x WHERE x.manager_id=g.manager_id) FROM coverage_groups g GROUP BY g.manager_id ORDER BY g.manager_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoverageTotals
	for rows.Next() {
		var c CoverageTotals
		var oldest, newest sql.NullInt64
		if err := rows.Scan(&c.ManagerID, &c.Wanted, &c.Have, &c.Groups, &c.GroupsRead, &oldest, &newest, &c.Gaps); err != nil {
			return nil, err
		}
		c.OldestRead, c.NewestRead = ptrMS(oldest), ptrMS(newest)
		out = append(out, c)
	}
	return out, rows.Err()
}

// CoverageDay is one day's coverage at its last complete sweep.
type CoverageDay struct {
	Day    string    `json:"day"`
	Wanted int       `json:"wanted"`
	Have   int       `json:"have"`
	Groups int       `json:"groups"`
	At     time.Time `json:"at"`
}

// CoverageTrend returns a manager's daily coverage, oldest first, from the
// last days days.
func (s *Store) CoverageTrend(ctx context.Context, managerID string, days int, now time.Time) ([]CoverageDay, error) {
	from := now.UTC().AddDate(0, 0, -days).Format("2006-01-02")
	rows, err := s.r.QueryContext(ctx, `SELECT day, wanted, have, groups, at FROM coverage_daily WHERE manager_id=? AND day>? ORDER BY day`, managerID, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoverageDay
	for rows.Next() {
		var d CoverageDay
		var at int64
		if err := rows.Scan(&d.Day, &d.Wanted, &d.Have, &d.Groups, &at); err != nil {
			return nil, err
		}
		d.At = fromMS(at)
		out = append(out, d)
	}
	return out, rows.Err()
}

// CoverageGap is a wanted item without a file.
type CoverageGap struct {
	ManagerID  string     `json:"manager_id"`
	ContentKey string     `json:"content_key"`
	GroupID    int64      `json:"group_id"`
	Label      string     `json:"label"`
	AiredAt    *time.Time `json:"aired_at,omitempty"`
	FirstSeen  time.Time  `json:"first_seen"`
	LastSeen   time.Time  `json:"last_seen"`
}

// CoverageGaps pages a manager's gaps, most recently aired first.
func (s *Store) CoverageGaps(ctx context.Context, managerID string, offset, limit int) ([]CoverageGap, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT manager_id, content_key, group_id, label, aired_at, first_seen, last_seen FROM coverage_gaps
		WHERE manager_id=? ORDER BY aired_at IS NULL, aired_at DESC, content_key LIMIT ? OFFSET ?`, managerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoverageGap
	for rows.Next() {
		var g CoverageGap
		var aired sql.NullInt64
		var first, last int64
		if err := rows.Scan(&g.ManagerID, &g.ContentKey, &g.GroupID, &g.Label, &aired, &first, &last); err != nil {
			return nil, err
		}
		g.AiredAt, g.FirstSeen, g.LastSeen = ptrMS(aired), fromMS(first), fromMS(last)
		out = append(out, g)
	}
	return out, rows.Err()
}

// LastGrabs returns, for every content key of a manager with a recorded
// grab in the manager history, the time of its latest grab.
func (s *Store) LastGrabs(ctx context.Context, managerID string) (map[string]time.Time, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT content_key, MAX(at) FROM manager_history WHERE manager_id=? AND event_type='grabbed' AND content_key IS NOT NULL
		GROUP BY content_key`, managerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var k string
		var at int64
		if err := rows.Scan(&k, &at); err != nil {
			return nil, err
		}
		out[k] = fromMS(at)
	}
	return out, rows.Err()
}
