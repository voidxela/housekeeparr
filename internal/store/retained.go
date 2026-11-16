package store

import (
	"context"
	"database/sql"
	"time"
)

// RetainedTransfer is a transfer a workflow stopped and kept with its data.
type RetainedTransfer struct {
	ClientID       string     `json:"client_id"`
	NativeID       string     `json:"native_id"`
	IntentID       int64      `json:"intent_id"`
	Name           string     `json:"name,omitempty"`
	ManagerID      string     `json:"manager_id,omitempty"`
	Content        string     `json:"content,omitempty"`
	WantedBytes    *int64     `json:"wanted_bytes,omitempty"`
	VerifiedBytes  *int64     `json:"verified_bytes,omitempty"`
	RetainedAt     time.Time  `json:"retained_at"`
	ReleasedAt     *time.Time `json:"released_at,omitempty"`
	ReleasedReason string     `json:"released_reason,omitempty"`
}

// RetainedUsage summarises transfers still retained.
type RetainedUsage struct {
	Count int `json:"count"`
	// KnownBytes sums the logical wanted size of retained transfers whose
	// size is known; UnknownSize counts the others.
	KnownBytes  int64 `json:"known_bytes"`
	UnknownSize int   `json:"unknown_size"`
}

func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

// RecordRetained records a retained transfer; recording it again for the
// same intent is a no-op.
func (t *Tx) RecordRetained(r RetainedTransfer) error {
	_, err := t.exec(`INSERT OR IGNORE INTO retained_transfers(client_id, native_id, intent_id, name, manager_id, content, wanted_bytes,
		verified_bytes, retained_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, r.ClientID, r.NativeID, r.IntentID, r.Name, r.ManagerID, r.Content,
		nullInt(r.WantedBytes), nullInt(r.VerifiedBytes), ms(r.RetainedAt))
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
	q := `SELECT client_id, native_id, intent_id, name, manager_id, content, wanted_bytes, verified_bytes, retained_at, released_at,
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
		if err := rows.Scan(&r.ClientID, &r.NativeID, &r.IntentID, &r.Name, &r.ManagerID, &r.Content, &wanted, &verified, &at, &rel, &r.ReleasedReason); err != nil {
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
// more than one intent counts once.
func (s *Store) RetainedUsage(ctx context.Context) (RetainedUsage, error) {
	var u RetainedUsage
	rows, err := s.r.QueryContext(ctx, `SELECT max(wanted_bytes) FROM retained_transfers WHERE released_at IS NULL GROUP BY client_id, native_id`)
	if err != nil {
		return u, err
	}
	defer rows.Close()
	for rows.Next() {
		var b sql.NullInt64
		if err := rows.Scan(&b); err != nil {
			return u, err
		}
		u.Count++
		if b.Valid {
			u.KnownBytes += b.Int64
		} else {
			u.UnknownSize++
		}
	}
	return u, rows.Err()
}
