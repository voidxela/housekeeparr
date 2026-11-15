package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Run is one process that owned the store.
type Run struct {
	RunID               string     `json:"run_id"`
	Kind                string     `json:"kind"`
	StartedAt           time.Time  `json:"started_at"`
	PID                 int        `json:"pid"`
	Version             string     `json:"version"`
	ConfigRevision      string     `json:"config_revision"`
	Mode                string     `json:"mode"`
	HeartbeatAt         time.Time  `json:"heartbeat_at"`
	LastCycleStartedAt  *time.Time `json:"last_cycle_started_at,omitempty"`
	LastCycleFinishedAt *time.Time `json:"last_cycle_finished_at,omitempty"`
	NextCycleAt         *time.Time `json:"next_cycle_at,omitempty"`
	StoppedAt           *time.Time `json:"stopped_at,omitempty"`
	StopReason          string     `json:"stop_reason,omitempty"`
}

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// StartRun records process start. Runs that never stopped cleanly are
// closed first, so abrupt terminations remain visible in history.
func (t *Tx) StartRun(r Run) (abandoned int64, err error) {
	res, err := t.exec(`UPDATE runs SET stopped_at = heartbeat_at, stop_reason = 'terminated without clean shutdown'
		WHERE stopped_at IS NULL`)
	if err != nil {
		return 0, err
	}
	abandoned, _ = res.RowsAffected()
	_, err = t.exec(`INSERT INTO runs(run_id, kind, started_at, pid, version, config_revision, mode, heartbeat_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.Kind, ms(r.StartedAt), r.PID, r.Version, r.ConfigRevision, r.Mode, ms(r.StartedAt))
	return abandoned, err
}

// Heartbeat updates the worker heartbeat.
func (t *Tx) Heartbeat(runID string, at time.Time) error {
	_, err := t.exec("UPDATE runs SET heartbeat_at=? WHERE run_id=?", ms(at), runID)
	return err
}

// CycleStarted records the start of an observation cycle.
func (t *Tx) CycleStarted(runID string, at time.Time) error {
	_, err := t.exec("UPDATE runs SET heartbeat_at=?, last_cycle_started_at=? WHERE run_id=?", ms(at), ms(at), runID)
	return err
}

// CycleFinished records completion and the next scheduled cycle.
func (t *Tx) CycleFinished(runID string, at time.Time, next *time.Time) error {
	_, err := t.exec("UPDATE runs SET heartbeat_at=?, last_cycle_finished_at=?, next_cycle_at=? WHERE run_id=?",
		ms(at), ms(at), nullMS(next), runID)
	return err
}

// StopRun records a clean shutdown.
func (t *Tx) StopRun(runID string, at time.Time, reason string) error {
	_, err := t.exec("UPDATE runs SET stopped_at=?, stop_reason=?, heartbeat_at=?, next_cycle_at=NULL WHERE run_id=?",
		ms(at), reason, ms(at), runID)
	return err
}

const runCols = `run_id, kind, started_at, pid, version, config_revision, mode, heartbeat_at,
	last_cycle_started_at, last_cycle_finished_at, next_cycle_at, stopped_at, stop_reason`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var started, hb int64
	var lcs, lcf, next, stopped sql.NullInt64
	var reason sql.NullString
	err := sc.Scan(&r.RunID, &r.Kind, &started, &r.PID, &r.Version, &r.ConfigRevision, &r.Mode, &hb,
		&lcs, &lcf, &next, &stopped, &reason)
	if err != nil {
		return r, err
	}
	r.StartedAt, r.HeartbeatAt = fromMS(started), fromMS(hb)
	r.LastCycleStartedAt, r.LastCycleFinishedAt, r.NextCycleAt, r.StoppedAt = ptrMS(lcs), ptrMS(lcf), ptrMS(next), ptrMS(stopped)
	r.StopReason = reason.String
	return r, nil
}

// LatestRun returns the most recently started run, or nil.
func (s *Store) LatestRun(ctx context.Context) (*Run, error) {
	r, err := scanRun(s.r.QueryRowContext(ctx, "SELECT "+runCols+" FROM runs ORDER BY started_at DESC, rowid DESC LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// RecentRuns lists recent runs, newest first.
func (s *Store) RecentRuns(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT "+runCols+" FROM runs ORDER BY started_at DESC, rowid DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuditEvent is an operator-relevant event.
type AuditEvent struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail"`
}

// Audit appends an audit event.
func (t *Tx) Audit(at time.Time, actor, kind, detail string) error {
	_, err := t.exec("INSERT INTO audit_events(at, actor, kind, detail) VALUES(?, ?, ?, ?)", ms(at), actor, kind, detail)
	return err
}

// RecentAudit lists audit events, newest first.
func (s *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEvent, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT id, at, actor, kind, detail FROM audit_events ORDER BY at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Kind, &e.Detail); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
