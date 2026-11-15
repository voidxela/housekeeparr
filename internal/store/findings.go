package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

const findingCols = `id, dedup_key, type, severity, state, title, summary, subject_json, detail_json, policy_id, policy_revision,
	first_seen, last_observed, last_evaluated, evidence_at, next_evaluation_at, acknowledged_at, snoozed_until,
	resolved_at, resolution, previous_id, occurrence`

func scanFinding(sc interface{ Scan(...any) error }) (domain.Finding, error) {
	var f domain.Finding
	var subj, det string
	var pid, prev sql.NullString
	var fs, lo, le, ea int64
	var ne, ack, snooze, res sql.NullInt64
	var resolution sql.NullString
	var prevID sql.NullInt64
	var sev, state string
	if err := sc.Scan(&f.ID, &f.DedupKey, &f.Type, &sev, &state, &f.Title, &f.Summary, &subj, &det, &pid, &prev,
		&fs, &lo, &le, &ea, &ne, &ack, &snooze, &res, &resolution, &prevID, &f.Occurrence); err != nil {
		return f, err
	}
	f.Severity, f.State = domain.Severity(sev), domain.FindingState(state)
	f.PolicyID, f.PolicyRevision = pid.String, prev.String
	f.FirstSeen, f.LastObserved, f.LastEvaluated, f.EvidenceAt = fromMS(fs), fromMS(lo), fromMS(le), fromMS(ea)
	f.NextEvaluationAt, f.AcknowledgedAt, f.SnoozedUntil, f.ResolvedAt = ptrMS(ne), ptrMS(ack), ptrMS(snooze), ptrMS(res)
	f.Resolution = resolution.String
	if prevID.Valid {
		v := prevID.Int64
		f.PreviousID = &v
	}
	if err := json.Unmarshal([]byte(subj), &f.Subject); err != nil {
		return f, err
	}
	if err := json.Unmarshal([]byte(det), &f.Detail); err != nil {
		return f, err
	}
	return f, nil
}

func scanFindings(rows *sql.Rows, err error) ([]domain.Finding, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ActiveFinding returns the unresolved finding for a key, or nil.
func (t *Tx) ActiveFinding(key string) (*domain.Finding, error) {
	f, err := scanFinding(t.queryRow("SELECT "+findingCols+" FROM findings WHERE dedup_key=? AND state != 'resolved'", key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// LatestFinding returns the newest finding (any state) for a key, or nil.
func (t *Tx) LatestFinding(key string) (*domain.Finding, error) {
	f, err := scanFinding(t.queryRow("SELECT "+findingCols+" FROM findings WHERE dedup_key=? ORDER BY id DESC LIMIT 1", key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// ActiveFindingsWithPrefix lists unresolved findings whose key has a prefix.
func (t *Tx) ActiveFindingsWithPrefix(prefix string) ([]domain.Finding, error) {
	// Escape LIKE wildcards in the prefix.
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
	return scanFindings(t.query("SELECT "+findingCols+` FROM findings WHERE state != 'resolved' AND dedup_key LIKE ? ESCAPE '\' ORDER BY id`, esc+"%"))
}

// ExpiredSnoozes lists snoozed findings whose snooze has ended.
func (t *Tx) ExpiredSnoozes(now time.Time) ([]domain.Finding, error) {
	return scanFindings(t.query("SELECT "+findingCols+" FROM findings WHERE state='snoozed' AND snoozed_until <= ?", ms(now)))
}

// GetFinding returns a finding by ID inside a transaction.
func (t *Tx) GetFinding(id int64) (*domain.Finding, error) {
	f, err := scanFinding(t.queryRow("SELECT "+findingCols+" FROM findings WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func findingArgs(f *domain.Finding) ([]any, error) {
	subj, err := json.Marshal(f.Subject)
	if err != nil {
		return nil, err
	}
	det, err := json.Marshal(f.Detail)
	if err != nil {
		return nil, err
	}
	var prev sql.NullInt64
	if f.PreviousID != nil {
		prev = sql.NullInt64{Int64: *f.PreviousID, Valid: true}
	}
	return []any{f.DedupKey, f.Type, string(f.Severity), string(f.State), f.Title, f.Summary, string(subj), string(det),
		nullStr(f.PolicyID), nullStr(f.PolicyRevision), ms(f.FirstSeen), ms(f.LastObserved), ms(f.LastEvaluated), ms(f.EvidenceAt),
		nullMS(f.NextEvaluationAt), nullMS(f.AcknowledgedAt), nullMS(f.SnoozedUntil), nullMS(f.ResolvedAt),
		nullStr(f.Resolution), prev, f.Occurrence}, nil
}

// InsertFinding stores a new finding and sets its ID.
func (t *Tx) InsertFinding(f *domain.Finding) error {
	args, err := findingArgs(f)
	if err != nil {
		return err
	}
	res, err := t.exec(`INSERT INTO findings(dedup_key, type, severity, state, title, summary, subject_json, detail_json,
		policy_id, policy_revision, first_seen, last_observed, last_evaluated, evidence_at, next_evaluation_at,
		acknowledged_at, snoozed_until, resolved_at, resolution, previous_id, occurrence)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	if err != nil {
		return err
	}
	f.ID, err = res.LastInsertId()
	return err
}

// UpdateFinding replaces a finding's mutable fields.
func (t *Tx) UpdateFinding(f *domain.Finding) error {
	args, err := findingArgs(f)
	if err != nil {
		return err
	}
	args = append(args, f.ID)
	_, err = t.exec(`UPDATE findings SET dedup_key=?, type=?, severity=?, state=?, title=?, summary=?, subject_json=?, detail_json=?,
		policy_id=?, policy_revision=?, first_seen=?, last_observed=?, last_evaluated=?, evidence_at=?, next_evaluation_at=?,
		acknowledged_at=?, snoozed_until=?, resolved_at=?, resolution=?, previous_id=?, occurrence=? WHERE id=?`, args...)
	return err
}

// AddFindingEvent appends a lifecycle event.
func (t *Tx) AddFindingEvent(e domain.FindingEvent) error {
	_, err := t.exec("INSERT INTO finding_events(finding_id, at, kind, actor, detail) VALUES(?, ?, ?, ?, ?)",
		e.FindingID, ms(e.At), e.Kind, e.Actor, e.Detail)
	return err
}

// FindingFilter narrows finding listings.
type FindingFilter struct {
	States   []domain.FindingState
	Type     string
	ClientID string
	NativeID string
	Limit    int
}

// Findings lists findings, most severe and most recent first.
func (s *Store) Findings(ctx context.Context, f FindingFilter) ([]domain.Finding, error) {
	q := "SELECT " + findingCols + " FROM findings WHERE 1=1"
	var args []any
	if len(f.States) > 0 {
		q += " AND state IN (" + strings.TrimSuffix(strings.Repeat("?,", len(f.States)), ",") + ")"
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	if f.Type != "" {
		q += " AND type=?"
		args = append(args, f.Type)
	}
	if f.ClientID != "" {
		q += " AND json_extract(subject_json, '$.client_id')=? AND json_extract(subject_json, '$.native_id')=?"
		args = append(args, f.ClientID, f.NativeID)
	}
	q += ` ORDER BY CASE state WHEN 'resolved' THEN 1 ELSE 0 END,
		CASE severity WHEN 'error' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
		COALESCE(resolved_at, last_observed) DESC, id DESC`
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	return scanFindings(s.r.QueryContext(ctx, q, args...))
}

// Finding returns one finding or nil.
func (s *Store) Finding(ctx context.Context, id int64) (*domain.Finding, error) {
	f, err := scanFinding(s.r.QueryRowContext(ctx, "SELECT "+findingCols+" FROM findings WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// FindingHistory returns earlier occurrences linked by previous_id.
func (s *Store) FindingHistory(ctx context.Context, f domain.Finding, limit int) ([]domain.Finding, error) {
	var out []domain.Finding
	prev := f.PreviousID
	for prev != nil && len(out) < limit {
		p, err := s.Finding(ctx, *prev)
		if err != nil || p == nil {
			return out, err
		}
		out = append(out, *p)
		prev = p.PreviousID
	}
	return out, nil
}

func scanEvents(rows *sql.Rows, err error) ([]domain.FindingEvent, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FindingEvent
	for rows.Next() {
		var e domain.FindingEvent
		var at int64
		if err := rows.Scan(&e.ID, &e.FindingID, &at, &e.Kind, &e.Actor, &e.Detail); err != nil {
			return nil, err
		}
		e.At = fromMS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// FindingEvents returns a finding's lifecycle, oldest first.
func (s *Store) FindingEvents(ctx context.Context, id int64) ([]domain.FindingEvent, error) {
	return scanEvents(s.r.QueryContext(ctx, "SELECT id, finding_id, at, kind, actor, detail FROM finding_events WHERE finding_id=? ORDER BY at, id", id))
}

// RecentFindingEvents returns lifecycle events across findings, newest first.
func (s *Store) RecentFindingEvents(ctx context.Context, limit int) ([]domain.FindingEvent, error) {
	return scanEvents(s.r.QueryContext(ctx, "SELECT id, finding_id, at, kind, actor, detail FROM finding_events ORDER BY at DESC, id DESC LIMIT ?", limit))
}

// FindingCounts counts unresolved findings by state and severity.
type FindingCounts struct {
	Open, Acknowledged, Snoozed int
	BySeverity                  map[domain.Severity]int // open + acknowledged
}

// CountFindings summarises unresolved findings.
func (s *Store) CountFindings(ctx context.Context) (FindingCounts, error) {
	c := FindingCounts{BySeverity: map[domain.Severity]int{}}
	rows, err := s.r.QueryContext(ctx, "SELECT state, severity, count(*) FROM findings WHERE state != 'resolved' GROUP BY state, severity")
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var st, sev string
		var n int
		if err := rows.Scan(&st, &sev, &n); err != nil {
			return c, err
		}
		switch domain.FindingState(st) {
		case domain.FindingOpen:
			c.Open += n
		case domain.FindingAcknowledged:
			c.Acknowledged += n
		case domain.FindingSnoozed:
			c.Snoozed += n
		}
		if domain.FindingState(st) != domain.FindingSnoozed {
			c.BySeverity[domain.Severity(sev)] += n
		}
	}
	return c, rows.Err()
}
