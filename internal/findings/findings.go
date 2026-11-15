// Package findings owns the finding lifecycle: deduplication, updates,
// recurrence, acknowledgement, snoozing, and evidence-based resolution.
// Callers supply drafts produced by the decision layer; this package never
// decides whether a condition exists.
package findings

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Draft is a finding as currently assessed.
type Draft struct {
	DedupKey         string
	Type             string
	Severity         domain.Severity
	Title            string
	Summary          string
	Subject          domain.Subject
	Detail           domain.FindingDetail
	PolicyID         string
	PolicyRevision   string
	EvidenceAt       time.Time
	NextEvaluationAt *time.Time
}

// Resolution explains why an active finding is no longer drafted.
type Resolution struct {
	Reason string
	// Limitation is appended to the finding when the cause is not known,
	// e.g. a transfer that disappeared for an unknown reason.
	Limitation string
}

// Evaluation is one authoritative assessment of a scope. Every active
// finding whose dedup key starts with Scope and is neither drafted nor
// kept is resolved. Callers must only build an Evaluation from fresh,
// successful, complete evidence; a failed read produces no Evaluation.
type Evaluation struct {
	Scope   string
	Drafts  []Draft
	Keep    map[string]bool // keys in scope that could not be re-evaluated
	Resolve func(f domain.Finding) Resolution
	Actor   string
}

// Result summarises changes for logging and notification.
type Result struct {
	Opened, Recurred, Updated, Resolved []domain.Finding
}

// Apply reconciles drafts with persisted findings in one transaction.
func Apply(tx *store.Tx, ev Evaluation, now time.Time) (Result, error) {
	var res Result
	actor := ev.Actor
	if actor == "" {
		actor = "system"
	}
	drafted := map[string]bool{}
	for _, d := range ev.Drafts {
		if !strings.HasPrefix(d.DedupKey, ev.Scope) {
			return res, fmt.Errorf("draft %q outside evaluation scope %q", d.DedupKey, ev.Scope)
		}
		if drafted[d.DedupKey] {
			return res, fmt.Errorf("duplicate draft %q", d.DedupKey)
		}
		drafted[d.DedupKey] = true
		active, err := tx.ActiveFinding(d.DedupKey)
		if err != nil {
			return res, err
		}
		if active == nil {
			f, recurred, err := open(tx, d, now, actor)
			if err != nil {
				return res, err
			}
			if recurred {
				res.Recurred = append(res.Recurred, f)
			} else {
				res.Opened = append(res.Opened, f)
			}
			continue
		}
		changed, err := update(tx, active, d, now, actor)
		if err != nil {
			return res, err
		}
		if changed {
			res.Updated = append(res.Updated, *active)
		}
	}
	actives, err := tx.ActiveFindingsWithPrefix(ev.Scope)
	if err != nil {
		return res, err
	}
	for _, f := range actives {
		if drafted[f.DedupKey] || ev.Keep[f.DedupKey] {
			continue
		}
		r := Resolution{Reason: "Condition no longer observed in a successful evaluation."}
		if ev.Resolve != nil {
			r = ev.Resolve(f)
		}
		if err := resolve(tx, &f, r, now, actor); err != nil {
			return res, err
		}
		res.Resolved = append(res.Resolved, f)
	}
	return res, nil
}

func open(tx *store.Tx, d Draft, now time.Time, actor string) (domain.Finding, bool, error) {
	prev, err := tx.LatestFinding(d.DedupKey)
	if err != nil {
		return domain.Finding{}, false, err
	}
	f := domain.Finding{
		DedupKey: d.DedupKey, Type: d.Type, Severity: d.Severity, State: domain.FindingOpen,
		Title: d.Title, Summary: d.Summary, Subject: d.Subject, Detail: d.Detail,
		PolicyID: d.PolicyID, PolicyRevision: d.PolicyRevision,
		FirstSeen: now, LastObserved: now, LastEvaluated: now, EvidenceAt: d.EvidenceAt,
		NextEvaluationAt: d.NextEvaluationAt, Occurrence: 1,
	}
	kind, detail := "opened", d.Title
	if prev != nil {
		id := prev.ID
		f.PreviousID = &id
		f.Occurrence = prev.Occurrence + 1
		kind = "recurred"
		detail = fmt.Sprintf("%s (occurrence %d; previous finding #%d resolved %s)", d.Title, f.Occurrence, prev.ID, fmtTime(prev.ResolvedAt))
	}
	if err := tx.InsertFinding(&f); err != nil {
		return f, false, err
	}
	return f, prev != nil, tx.AddFindingEvent(domain.FindingEvent{FindingID: f.ID, At: now, Kind: kind, Actor: actor, Detail: detail})
}

func update(tx *store.Tx, f *domain.Finding, d Draft, now time.Time, actor string) (bool, error) {
	var changes []string
	if f.Severity != d.Severity {
		changes = append(changes, fmt.Sprintf("severity %s → %s", f.Severity, d.Severity))
	}
	if f.Title != d.Title {
		changes = append(changes, fmt.Sprintf("now: %s", d.Title))
	}
	if f.Detail.CurrentState != d.Detail.CurrentState {
		switch {
		case d.Detail.CurrentState == "":
			changes = append(changes, "condition re-established on current evidence")
		default:
			changes = append(changes, "current state: "+d.Detail.CurrentState)
		}
	}
	if f.PolicyRevision != d.PolicyRevision && f.PolicyRevision != "" {
		changes = append(changes, fmt.Sprintf("policy revision %s → %s", f.PolicyRevision, d.PolicyRevision))
	}
	f.Type, f.Severity, f.Title, f.Summary = d.Type, d.Severity, d.Title, d.Summary
	f.Subject, f.Detail = d.Subject, d.Detail
	f.PolicyID, f.PolicyRevision = d.PolicyID, d.PolicyRevision
	f.LastObserved, f.LastEvaluated, f.EvidenceAt = now, now, d.EvidenceAt
	f.NextEvaluationAt = d.NextEvaluationAt
	if err := tx.UpdateFinding(f); err != nil {
		return false, err
	}
	if len(changes) == 0 {
		return false, nil
	}
	return true, tx.AddFindingEvent(domain.FindingEvent{FindingID: f.ID, At: now, Kind: "updated", Actor: actor, Detail: strings.Join(changes, "; ")})
}

func resolve(tx *store.Tx, f *domain.Finding, r Resolution, now time.Time, actor string) error {
	f.State = domain.FindingResolved
	f.ResolvedAt = &now
	f.LastEvaluated = now
	f.Resolution = r.Reason
	f.SnoozedUntil = nil
	f.NextEvaluationAt = nil
	if r.Limitation != "" {
		f.Detail.Limitations = append(f.Detail.Limitations, r.Limitation)
	}
	if err := tx.UpdateFinding(f); err != nil {
		return err
	}
	return tx.AddFindingEvent(domain.FindingEvent{FindingID: f.ID, At: now, Kind: "resolved", Actor: actor, Detail: r.Reason})
}

// Errors returned by operator actions.
var (
	ErrNotFound      = errors.New("finding not found")
	ErrResolved      = errors.New("finding is already resolved")
	ErrInvalidSnooze = errors.New("snooze duration must be between 1 hour and 90 days")
)

// MinSnooze and MaxSnooze bound snooze durations.
const (
	MinSnooze = time.Hour
	MaxSnooze = 90 * 24 * time.Hour
)

func load(tx *store.Tx, id int64) (*domain.Finding, error) {
	f, err := tx.GetFinding(id)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, ErrNotFound
	}
	if f.State == domain.FindingResolved {
		return f, ErrResolved
	}
	return f, nil
}

// Acknowledge marks a finding as seen. It does not resolve the condition.
func Acknowledge(tx *store.Tx, id int64, actor string, now time.Time) (*domain.Finding, error) {
	f, err := load(tx, id)
	if err != nil {
		return f, err
	}
	if f.State == domain.FindingAcknowledged {
		return f, nil
	}
	f.State = domain.FindingAcknowledged
	f.AcknowledgedAt = &now
	f.SnoozedUntil = nil
	if err := tx.UpdateFinding(f); err != nil {
		return f, err
	}
	return f, tx.AddFindingEvent(domain.FindingEvent{FindingID: id, At: now, Kind: "acknowledged", Actor: actor,
		Detail: "Acknowledged; the finding stays visible until the condition is resolved."})
}

// Reopen returns an acknowledged or snoozed finding to open.
func Reopen(tx *store.Tx, id int64, actor string, now time.Time) (*domain.Finding, error) {
	f, err := load(tx, id)
	if err != nil {
		return f, err
	}
	if f.State == domain.FindingOpen {
		return f, nil
	}
	kind := "unacknowledged"
	if f.State == domain.FindingSnoozed {
		kind = "unsnoozed"
	}
	f.State = domain.FindingOpen
	f.AcknowledgedAt, f.SnoozedUntil = nil, nil
	if err := tx.UpdateFinding(f); err != nil {
		return f, err
	}
	return f, tx.AddFindingEvent(domain.FindingEvent{FindingID: id, At: now, Kind: kind, Actor: actor, Detail: "Returned to open."})
}

// Snooze suppresses notifications and automation for a finding until the
// expiry. Observation and evaluation continue.
func Snooze(tx *store.Tx, id int64, d time.Duration, actor string, now time.Time) (*domain.Finding, error) {
	if d < MinSnooze || d > MaxSnooze {
		return nil, ErrInvalidSnooze
	}
	f, err := load(tx, id)
	if err != nil {
		return f, err
	}
	until := now.Add(d)
	f.State = domain.FindingSnoozed
	f.SnoozedUntil = &until
	if err := tx.UpdateFinding(f); err != nil {
		return f, err
	}
	return f, tx.AddFindingEvent(domain.FindingEvent{FindingID: id, At: now, Kind: "snoozed", Actor: actor,
		Detail: fmt.Sprintf("Snoozed until %s. Observation continues; notifications and automation for this finding are suppressed.", until.UTC().Format(time.RFC3339))})
}

// ExpireSnoozes reopens findings whose snooze has ended.
func ExpireSnoozes(tx *store.Tx, now time.Time) (int, error) {
	list, err := tx.ExpiredSnoozes(now)
	if err != nil {
		return 0, err
	}
	for i := range list {
		f := &list[i]
		f.State = domain.FindingOpen
		f.SnoozedUntil = nil
		if err := tx.UpdateFinding(f); err != nil {
			return i, err
		}
		if err := tx.AddFindingEvent(domain.FindingEvent{FindingID: f.ID, At: now, Kind: "snooze_expired", Actor: "system", Detail: "Snooze expired; finding is open again."}); err != nil {
			return i, err
		}
	}
	return len(list), nil
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "at an unknown time"
	}
	return t.UTC().Format(time.RFC3339)
}
