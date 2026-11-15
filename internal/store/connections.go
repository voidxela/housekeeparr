package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Connection status values.
const (
	ConnUnknown  = "unknown"
	ConnOK       = "ok"
	ConnFailing  = "failing"
	ConnDisabled = "disabled"
)

// ConnectionRecord is the persisted health of one connection.
type ConnectionRecord struct {
	ID                  string                 `json:"id"`
	Kind                string                 `json:"kind"`
	Name                string                 `json:"name"`
	BaseURL             string                 `json:"base_url"`
	Status              string                 `json:"status"`
	Configured          bool                   `json:"configured"`
	LastAttemptAt       *time.Time             `json:"last_attempt_at,omitempty"`
	LastSuccessAt       *time.Time             `json:"last_success_at,omitempty"`
	ConsecutiveFailures int                    `json:"consecutive_failures"`
	LastErrorClass      string                 `json:"last_error_class,omitempty"`
	LastError           string                 `json:"last_error,omitempty"`
	NextAttemptAt       *time.Time             `json:"next_attempt_at,omitempty"`
	Probe               *adapter.Probe         `json:"probe,omitempty"`
	ProbedAt            *time.Time             `json:"probed_at,omitempty"`
	Health              *domain.HealthSnapshot `json:"health,omitempty"`
	HealthAt            *time.Time             `json:"health_at,omitempty"`
}

// ConnectionCheck is one recorded attempt.
type ConnectionCheck struct {
	At         time.Time `json:"at"`
	Op         string    `json:"op"`
	OK         bool      `json:"ok"`
	ErrorClass string    `json:"error_class,omitempty"`
	Message    string    `json:"message,omitempty"`
	DurationMS int64     `json:"duration_ms"`
}

// SyncConnections registers configured connections and marks others as no
// longer configured (their history is retained).
func (t *Tx) SyncConnections(conns []ConnectionRecord, at time.Time) error {
	if _, err := t.exec("UPDATE connections SET configured=0"); err != nil {
		return err
	}
	for _, c := range conns {
		status := ConnUnknown
		if c.Status == ConnDisabled {
			status = ConnDisabled
		}
		_, err := t.exec(`INSERT INTO connections(id, kind, name, base_url, status, configured, updated_at)
			VALUES(?, ?, ?, ?, ?, 1, ?)
			ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, name=excluded.name, base_url=excluded.base_url,
				configured=1, updated_at=excluded.updated_at,
				status=CASE WHEN excluded.status='disabled' THEN 'disabled'
				            WHEN connections.status='disabled' THEN 'unknown' ELSE connections.status END`,
			c.ID, c.Kind, c.Name, c.BaseURL, status, ms(at))
		if err != nil {
			return err
		}
	}
	return nil
}

// RecordCheck appends an attempt to the bounded check history.
func (t *Tx) RecordCheck(id string, c ConnectionCheck) error {
	_, err := t.exec(`INSERT INTO connection_checks(connection_id, at, op, ok, error_class, message, duration_ms)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, id, ms(c.At), c.Op, c.OK, nullStr(c.ErrorClass), nullStr(c.Message), c.DurationMS)
	return err
}

// RecordSuccess marks a connection healthy after a successful read.
func (t *Tx) RecordSuccess(id string, at time.Time) error {
	_, err := t.exec(`UPDATE connections SET status='ok', last_attempt_at=?, last_success_at=?, consecutive_failures=0,
		last_error_class=NULL, last_error=NULL, next_attempt_at=NULL, updated_at=? WHERE id=?`, ms(at), ms(at), ms(at), id)
	return err
}

// RecordFailure marks a connection failing and schedules the next attempt.
func (t *Tx) RecordFailure(id string, at time.Time, class, msg string, next time.Time) (failures int, err error) {
	_, err = t.exec(`UPDATE connections SET status='failing', last_attempt_at=?, consecutive_failures=consecutive_failures+1,
		last_error_class=?, last_error=?, next_attempt_at=?, updated_at=? WHERE id=?`,
		ms(at), class, msg, ms(next), ms(at), id)
	if err != nil {
		return 0, err
	}
	err = t.queryRow("SELECT consecutive_failures FROM connections WHERE id=?", id).Scan(&failures)
	return failures, err
}

// SetProbe stores the latest successful probe.
func (t *Tx) SetProbe(id string, p adapter.Probe, at time.Time) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = t.exec("UPDATE connections SET probe_json=?, probed_at=? WHERE id=?", string(b), ms(at), id)
	return err
}

// SetHealth stores the latest manager health snapshot.
func (t *Tx) SetHealth(id string, h domain.HealthSnapshot) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = t.exec("UPDATE connections SET health_json=?, health_at=? WHERE id=?", string(b), ms(h.CollectedAt), id)
	return err
}

const connCols = `id, kind, name, base_url, status, configured, last_attempt_at, last_success_at, consecutive_failures,
	last_error_class, last_error, next_attempt_at, probe_json, probed_at, health_json, health_at`

func scanConn(sc interface{ Scan(...any) error }) (ConnectionRecord, error) {
	var c ConnectionRecord
	var la, ls, na, pa, ha sql.NullInt64
	var ec, le, pj, hj sql.NullString
	if err := sc.Scan(&c.ID, &c.Kind, &c.Name, &c.BaseURL, &c.Status, &c.Configured, &la, &ls, &c.ConsecutiveFailures,
		&ec, &le, &na, &pj, &pa, &hj, &ha); err != nil {
		return c, err
	}
	c.LastAttemptAt, c.LastSuccessAt, c.NextAttemptAt, c.ProbedAt, c.HealthAt = ptrMS(la), ptrMS(ls), ptrMS(na), ptrMS(pa), ptrMS(ha)
	c.LastErrorClass, c.LastError = ec.String, le.String
	if pj.Valid {
		var p adapter.Probe
		if err := json.Unmarshal([]byte(pj.String), &p); err == nil {
			c.Probe = &p
		}
	}
	if hj.Valid {
		var h domain.HealthSnapshot
		if err := json.Unmarshal([]byte(hj.String), &h); err == nil {
			c.Health = &h
		}
	}
	return c, nil
}

func listConnections(ctx context.Context, q querier) ([]ConnectionRecord, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+connCols+" FROM connections ORDER BY configured DESC, kind, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionRecord
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Connections lists all known connections.
func (s *Store) Connections(ctx context.Context) ([]ConnectionRecord, error) {
	return listConnections(ctx, s.r)
}

// Connection returns one connection or nil.
func (t *Tx) Connection(id string) (*ConnectionRecord, error) {
	c, err := scanConn(t.queryRow("SELECT "+connCols+" FROM connections WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// ConnectionChecks returns recent attempts for a connection, newest first.
func (s *Store) ConnectionChecks(ctx context.Context, id string, limit int) ([]ConnectionCheck, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT at, op, ok, error_class, message, duration_ms FROM connection_checks
		WHERE connection_id=? ORDER BY at DESC, id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionCheck
	for rows.Next() {
		var c ConnectionCheck
		var at int64
		var ec, msg sql.NullString
		if err := rows.Scan(&at, &c.Op, &c.OK, &ec, &msg, &c.DurationMS); err != nil {
			return nil, err
		}
		c.At, c.ErrorClass, c.Message = fromMS(at), ec.String, msg.String
		out = append(out, c)
	}
	return out, rows.Err()
}
