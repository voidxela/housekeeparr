package store

import (
	"context"
	"database/sql"
	"time"
)

// Notification is one event queued for one target (SPEC §11.2).
type Notification struct {
	ID        int64      `json:"id"`
	Target    string     `json:"target"`
	DedupKey  string     `json:"dedup_key"`
	Kind      string     `json:"kind"`
	FindingID *int64     `json:"finding_id,omitempty"`
	Severity  string     `json:"severity"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Link      string     `json:"link,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	NextAt    time.Time  `json:"next_at"`
	Attempts  int        `json:"attempts"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
	DroppedAt *time.Time `json:"dropped_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

// EnqueueNotification queues an event; an event with the same target and
// dedup key is never queued twice. It reports whether a row was added.
func (t *Tx) EnqueueNotification(n Notification) (bool, error) {
	res, err := t.exec(`INSERT OR IGNORE INTO notification_events(target, dedup_key, kind, finding_id, severity, title, body, link, created_at, next_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, n.Target, n.DedupKey, n.Kind, nullInt(n.FindingID), n.Severity, n.Title, n.Body, n.Link,
		ms(n.CreatedAt), ms(n.CreatedAt))
	if err != nil {
		return false, err
	}
	k, err := res.RowsAffected()
	return k > 0, err
}

// NotifiedFinding reports whether an "opened" event for the finding was
// delivered to the target (resolutions are notified only then).
func (t *Tx) NotifiedFinding(target string, findingID int64) (bool, error) {
	var n int
	err := t.tx.QueryRowContext(t.ctx, `SELECT count(*) FROM notification_events WHERE target=? AND finding_id=? AND kind='finding.opened' AND sent_at IS NOT NULL`,
		target, findingID).Scan(&n)
	return n > 0, err
}

const notificationCols = `id, target, dedup_key, kind, finding_id, severity, title, body, link, created_at, next_at, attempts, sent_at, dropped_at, last_error`

func scanNotifications(rows *sql.Rows) ([]Notification, error) {
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var fid, sent, dropped sql.NullInt64
		var created, next int64
		if err := rows.Scan(&n.ID, &n.Target, &n.DedupKey, &n.Kind, &fid, &n.Severity, &n.Title, &n.Body, &n.Link, &created, &next,
			&n.Attempts, &sent, &dropped, &n.LastError); err != nil {
			return nil, err
		}
		if fid.Valid {
			n.FindingID = &fid.Int64
		}
		n.CreatedAt, n.NextAt, n.SentAt, n.DroppedAt = fromMS(created), fromMS(next), ptrMS(sent), ptrMS(dropped)
		out = append(out, n)
	}
	return out, rows.Err()
}

// DueNotifications lists a target's undelivered events due by now, oldest
// first.
func (s *Store) DueNotifications(ctx context.Context, target string, now time.Time, limit int) ([]Notification, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT "+notificationCols+` FROM notification_events
		WHERE target=? AND sent_at IS NULL AND dropped_at IS NULL AND next_at <= ? ORDER BY id LIMIT ?`, target, ms(now), limit)
	if err != nil {
		return nil, err
	}
	return scanNotifications(rows)
}

// PendingNotifications counts a target's undelivered, undropped events.
func (s *Store) PendingNotifications(ctx context.Context, target string) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT count(*) FROM notification_events WHERE target=? AND sent_at IS NULL AND dropped_at IS NULL`, target).Scan(&n)
	return n, err
}

// ReserveNotificationAttempt spends hourly capacity before a webhook request.
func (t *Tx) ReserveNotificationAttempt(target string, at time.Time, maxPerHour int) (bool, error) {
	res, err := t.exec(`INSERT INTO notification_attempts(target, attempted_at)
		SELECT ?, ? WHERE (SELECT count(*) FROM notification_attempts WHERE target=? AND attempted_at >= ?) < ?`,
		target, ms(at), target, ms(at.Add(-time.Hour)), maxPerHour)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RecentNotifications lists the latest events across targets.
func (s *Store) RecentNotifications(ctx context.Context, limit int) ([]Notification, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT "+notificationCols+" FROM notification_events ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return scanNotifications(rows)
}

// MarkNotificationSent records a delivery.
func (t *Tx) MarkNotificationSent(id int64, now time.Time) error {
	_, err := t.exec(`UPDATE notification_events SET sent_at=?, attempts=attempts+1, last_error='' WHERE id=?`, ms(now), id)
	return err
}

// MarkNotificationFailed records a failed attempt and when to retry.
func (t *Tx) MarkNotificationFailed(id int64, now, next time.Time, errText string) error {
	_, err := t.exec(`UPDATE notification_events SET attempts=attempts+1, next_at=?, last_error=? WHERE id=?`, ms(next), errText, id)
	return err
}

// DropNotification gives up on an event, recording why.
func (t *Tx) DropNotification(id int64, now time.Time, reason string) error {
	_, err := t.exec(`UPDATE notification_events SET dropped_at=?, last_error=? WHERE id=? AND sent_at IS NULL`, ms(now), reason, id)
	return err
}

// ExpireNotifications records undelivered events that have exceeded their age limit.
func (t *Tx) ExpireNotifications(now time.Time, maxAge time.Duration) (int, error) {
	res, err := t.exec(`UPDATE notification_events SET dropped_at=?, last_error=?
		WHERE sent_at IS NULL AND dropped_at IS NULL AND created_at < ?`, ms(now),
		"not delivered within "+maxAge.String(), ms(now.Add(-maxAge)))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
