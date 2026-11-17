package store

import (
	"context"
	"database/sql"
	"time"
)

// Retained dispositions: what a workflow left behind.
const (
	RetainedStopped = "stopped" // stopped, kept with its data
	RetainedRunning = "running" // left active (keep-running, or a stop that did not take effect)
)

// RetainedTransfer is a transfer a workflow left in the client with its
// data after failing its grab: stopped, or deliberately left running.
type RetainedTransfer struct {
	ClientID       string     `json:"client_id"`
	NativeID       string     `json:"native_id"`
	IntentID       int64      `json:"intent_id"`
	Name           string     `json:"name,omitempty"`
	ManagerID      string     `json:"manager_id,omitempty"`
	Content        string     `json:"content,omitempty"`
	Disposition    string     `json:"disposition"`
	WantedBytes    *int64     `json:"wanted_bytes,omitempty"`
	VerifiedBytes  *int64     `json:"verified_bytes,omitempty"`
	RetainedAt     time.Time  `json:"retained_at"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	ReleasedReason string     `json:"released_reason,omitempty"`
}

// RetainedUsage summarises transfers still retained (SPEC §7.1).
type RetainedUsage struct {
	Count int `json:"count"`
	// Running counts those left active rather than stopped.
	Running int `json:"running"`
	// KnownBytes sums the logical wanted size of retained transfers whose
	// size is known. NoPayload counts those whose logical size is unknown
	// but which hold no payload (stopped before metadata arrived; they add
	// nothing to a byte total until they obtain metadata). UnknownSize
	// counts the rest: their contribution is unknown, never zero.
	KnownBytes  int64 `json:"known_bytes"`
	NoPayload   int   `json:"no_payload,omitempty"`
	UnknownSize int   `json:"unknown_size"`
}

func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// RecordRetained records a retained transfer. Recording it again for the
// same intent keeps the first record's details; a later verified stop
// changes a running disposition to stopped, never the reverse.
func (t *Tx) RecordRetained(r RetainedTransfer) error {
	if r.Disposition == "" {
		r.Disposition = RetainedStopped
	}
	_, err := t.exec(`INSERT INTO retained_transfers(client_id, native_id, intent_id, name, manager_id, content, wanted_bytes,
		verified_bytes, retained_at, disposition) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(client_id, native_id, intent_id) DO UPDATE SET disposition = CASE WHEN excluded.disposition = 'stopped'
			THEN 'stopped' ELSE disposition END,
			wanted_bytes = COALESCE(wanted_bytes, excluded.wanted_bytes), verified_bytes = COALESCE(verified_bytes, excluded.verified_bytes)`,
		r.ClientID, r.NativeID, r.IntentID, r.Name, r.ManagerID, r.Content, nullInt(r.WantedBytes), nullInt(r.VerifiedBytes), ms(r.RetainedAt), r.Disposition)
	return err
}

// RefreshRetained updates the sizes of a still-retained transfer from a
// fresh complete listing, so a transfer that is resumed (by the operator,
// or left running) and obtains metadata or downloads more is accounted
// for. Unknown values never replace known ones.
func (t *Tx) RefreshRetained(clientID, nativeID string, wanted, verified *int64) error {
	if wanted == nil && verified == nil {
		return nil
	}
	_, err := t.exec(`UPDATE retained_transfers SET wanted_bytes = COALESCE(?, wanted_bytes), verified_bytes = COALESCE(?, verified_bytes)
		WHERE client_id=? AND native_id=? AND released_at IS NULL`, nullInt(wanted), nullInt(verified), clientID, nativeID)
	return err
}

// ReleaseRetained marks a transfer no longer retained.
func (t *Tx) ReleaseRetained(clientID, nativeID, reason string, now time.Time) (int64, error) {
	res, err := t.exec(`UPDATE retained_transfers SET released_at=?, released_reason=? WHERE client_id=? AND native_id=? AND released_at IS NULL`,
		ms(now), reason, clientID, nativeID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RetainedTransfers lists retained transfers, newest first; with all set,
// released ones too.
func (s *Store) RetainedTransfers(ctx context.Context, all bool, limit int) ([]RetainedTransfer, error) {
	if limit <= 0 {
		limit = 500
	}
	q := `SELECT client_id, native_id, intent_id, name, manager_id, content, disposition, wanted_bytes, verified_bytes, retained_at, released_at,
		COALESCE(released_reason, '') FROM retained_transfers`
	if !all {
		q += " WHERE released_at IS NULL"
	}
	rows, err := s.r.QueryContext(ctx, q+" ORDER BY retained_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RetainedTransfer
	for rows.Next() {
		var r RetainedTransfer
		var wanted, verified, rel sql.NullInt64
		var at int64
		if err := rows.Scan(&r.ClientID, &r.NativeID, &r.IntentID, &r.Name, &r.ManagerID, &r.Content, &r.Disposition, &wanted, &verified, &at, &rel, &r.ReleasedReason); err != nil {
			return nil, err
		}
		r.RetainedAt, r.ReleasedAt = fromMS(at), ptrMS(rel)
		if wanted.Valid {
			r.WantedBytes = &wanted.Int64
		}
		if verified.Valid {
			r.VerifiedBytes = &verified.Int64
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RetainedUsage counts transfers still retained. A transfer retained by
// more than one intent counts once; it is running if any record left it
// running and no later one stopped it.
func (s *Store) RetainedUsage(ctx context.Context) (RetainedUsage, error) {
	var u RetainedUsage
	rows, err := s.r.QueryContext(ctx, `SELECT max(wanted_bytes), max(verified_bytes),
		(SELECT r2.disposition FROM retained_transfers r2 WHERE r2.client_id = r.client_id AND r2.native_id = r.native_id
			AND r2.released_at IS NULL ORDER BY r2.intent_id DESC LIMIT 1)
		FROM retained_transfers r WHERE released_at IS NULL GROUP BY client_id, native_id`)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var w, v sql.NullInt64
		var disp string
		if err := rows.Scan(&w, &v, &disp); err != nil {
			return u, err
		}
		u.Count++
		if disp == RetainedRunning {
			u.Running++
		}
		switch {
		case w.Valid:
			u.KnownBytes += w.Int64
		case v.Valid && v.Int64 == 0 && disp == RetainedStopped:
			u.NoPayload++
		default:
			u.UnknownSize++
		}
	}
	return u, rows.Err()
}
