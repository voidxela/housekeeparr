package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// ActionPreview is the shared CLI/web preview of what Housekeeparr would do
// about a finding: evidence, effective policy, every prerequisite, exact
// operations with their flags, and side effects.
type ActionPreview struct {
	FindingID int64            `json:"finding_id"`
	Workflow  string           `json:"workflow,omitempty"`
	Proposal  *action.Proposal `json:"proposal,omitempty"`
	NoAction  string           `json:"no_action,omitempty"`
	Policy    policy.Effective `json:"effective_policy"`
	Journal   JournalStatus    `json:"journal"`
}

// JournalStatus summarises the action journal for status displays.
type JournalStatus struct {
	RestoreHold *store.RestoreStatus `json:"restore_hold,omitempty"`
	Active      int                  `json:"active"`
	Uncertain   int                  `json:"uncertain"`
	Writable    bool                 `json:"writable"`
}

// Journal returns the action journal status.
func (s *Service) Journal(ctx context.Context) (JournalStatus, error) {
	js := JournalStatus{Writable: !s.st.ReadOnly()}
	rs, err := s.st.CheckRestore(ctx)
	if err != nil {
		return js, err
	}
	if rs.Suspected {
		js.RestoreHold = &rs
	}
	active, err := s.st.Intents(ctx, store.IntentFilter{States: []store.IntentState{store.IntentReserved, store.IntentExecuting, store.IntentReconciling}})
	if err != nil {
		return js, err
	}
	unc, err := s.st.Intents(ctx, store.IntentFilter{States: []store.IntentState{store.IntentUncertain}})
	if err != nil {
		return js, err
	}
	js.Active, js.Uncertain = len(active), len(unc)
	return js, nil
}

// Actions lists journaled actions, newest first.
func (s *Service) Actions(ctx context.Context, limit int) ([]store.Intent, error) {
	return s.st.Intents(ctx, store.IntentFilter{Limit: limit})
}

// Preview evaluates the action workflow for a finding without any external
// read or write: it uses local state only, like every page.
func (s *Service) Preview(ctx context.Context, findingID int64, origin action.Origin) (*ActionPreview, error) {
	f, err := s.st.Finding(ctx, findingID)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, ErrNotFound
	}
	out := &ActionPreview{FindingID: f.ID}
	if out.Journal, err = s.Journal(ctx); err != nil {
		return nil, err
	}
	in := action.PlanInput{Finding: *f, Origin: origin, PollInterval: s.cfg.Polling.Interval.Duration,
		ConfigRevision: s.cfg.Revision, Tested: action.Tested, Now: s.now()}
	sub := policy.Subject{ClientID: f.Subject.ClientID, NativeID: f.Subject.NativeID}
	if f.Subject.ClientID != "" && f.Subject.NativeID != "" {
		if in.Transfer, err = s.st.Transfer(ctx, f.Subject.ClientID, f.Subject.NativeID); err != nil {
			return nil, err
		}
		if in.Queue, err = s.st.QueueItems(ctx, store.QueueFilter{ClientID: f.Subject.ClientID, NativeID: f.Subject.NativeID}); err != nil {
			return nil, err
		}
	}
	if in.Transfer != nil {
		if c, ok := in.Transfer.Latest.Category.Get(); ok {
			sub.Category = c
		}
		switch p, ok := in.Transfer.Latest.Private.Get(); {
		case !ok:
			sub.Privacy = "unknown"
		case p:
			sub.Privacy = "private"
		default:
			sub.Privacy = "public"
		}
	}
	conns, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	var present []store.QueueRecord
	for _, q := range in.Queue {
		if q.Present {
			present = append(present, q)
		}
	}
	if len(present) == 1 {
		sub.ManagerID, sub.ContentKey = present[0].ManagerID, present[0].ItemKey
	}
	for _, c := range conns {
		c := c
		if c.ID == sub.ClientID {
			in.ClientKind, in.ClientProbe = adapter.Kind(c.Kind), c.Probe
		}
		if sub.ManagerID != "" && c.ID == sub.ManagerID {
			in.ManagerKind, in.ManagerProbe = adapter.Kind(c.Kind), c.Probe
		}
	}
	if sub.ManagerID != "" && !s.cfg.HasManager(sub.ManagerID) {
		// A manager removed from configuration: resolve without it.
		sub.ManagerID, sub.ContentKey = "", ""
	}
	if sub.ClientID != "" && !s.cfg.HasClient(sub.ClientID) {
		sub = policy.Subject{}
	}
	if in.Policy, err = s.cfg.Resolve(sub); err != nil {
		return nil, err
	}
	in.Scope = sub
	out.Policy = in.Policy
	for _, l := range []string{"transfer:" + f.Subject.ClientID + "|" + f.Subject.NativeID} {
		if in.LockHolder, err = s.st.LockHolder(ctx, l); err != nil {
			return nil, err
		}
		until, reason, ok, err := s.st.Cooldown(ctx, l)
		if err != nil {
			return nil, err
		}
		if ok {
			in.CooldownUntil, in.CooldownReason = &until, reason
		}
	}
	p, np := action.Plan(in)
	if np != nil {
		out.NoAction = np.Reason
		return out, nil
	}
	if out.Journal.RestoreHold != nil {
		p.Prerequisites = append(p.Prerequisites, action.Check{Name: "state matches the action journal", Hard: true, Detail: out.Journal.RestoreHold.Reason})
	}
	if !out.Journal.Writable {
		p.Prerequisites = append(p.Prerequisites, action.Check{Name: "writable state store", Hard: true,
			Detail: "this process opened the state store read-only (another Housekeeparr process owns it)"})
	}
	out.Workflow, out.Proposal = p.Workflow, p
	return out, nil
}

// ErrNoExecutor means no write executor exists for the workflow in this
// build; it is only reachable if every prerequisite were satisfied.
var ErrNoExecutor = errors.New("this build has no write executor for the workflow")

// Act runs a manual action for a finding through the same coordinator and
// prerequisites as automation. It re-plans from current local state, so a
// stale preview is never executed.
func (s *Service) Act(ctx context.Context, findingID int64, actor Actor) (*ActionPreview, *action.Result, error) {
	// Settle anything a crash interrupted before planning new work, so a
	// half-finished action holds its locks as uncertain rather than as
	// executing forever.
	if _, err := s.Reconcile(ctx); err != nil {
		return nil, nil, fmt.Errorf("reconcile interrupted actions: %w", err)
	}
	pv, err := s.Preview(ctx, findingID, action.OriginManual)
	if err != nil {
		return nil, nil, err
	}
	if pv.Proposal == nil {
		return pv, nil, &action.RefusedError{Err: action.ErrNotEligible, Checks: []action.Check{{Name: "workflow", Detail: pv.NoAction}}}
	}
	c := &action.Coordinator{Store: s.st, Executor: s.executor, Gate: gate{s}, Now: s.now, Actor: string(actor)}
	if c.Executor == nil {
		if b := pv.Proposal.Blockers(); len(b) > 0 {
			return pv, nil, &action.RefusedError{Err: action.ErrNotEligible, Checks: b}
		}
		return pv, nil, ErrNoExecutor
	}
	res, err := c.Execute(ctx, *pv.Proposal)
	return pv, &res, err
}

// Reconcile settles actions an interruption left in progress: it observes,
// never re-sends, and holds anything it cannot establish as uncertain.
// Without an executor nothing can be observed, so interrupted actions are
// only reported (they keep their locks).
func (s *Service) Reconcile(ctx context.Context) (action.ReconcileReport, error) {
	if s.executor == nil {
		return action.ReconcileReport{}, nil
	}
	c := &action.Coordinator{Store: s.st, Executor: s.executor, Now: s.now, Actor: "reconcile"}
	return c.Reconcile(ctx)
}

// Review resolves an uncertain action after operator inspection.
func (s *Service) Review(ctx context.Context, id int64, to store.IntentState, actor Actor, note string) error {
	c := &action.Coordinator{Store: s.st, Executor: s.executor, Now: s.now, Actor: string(actor)}
	return c.Review(ctx, id, to, string(actor), note)
}

// AcknowledgeRestore accepts a restored state database as authoritative.
func (s *Service) AcknowledgeRestore(ctx context.Context, actor Actor) error {
	return s.st.AcknowledgeRestore(ctx, string(actor))
}

// gate re-evaluates, before every write, the local conditions a proposal
// was planned under: the finding is still active, the state store is still
// consistent with the journal, and the effective policy for the proposal's
// scope is unchanged and still permits the workflow for its origin.
type gate struct{ s *Service }

func (g gate) Blockers(ctx context.Context, p action.Proposal) []action.Check {
	var out []action.Check
	block := func(name, detail string) {
		out = append(out, action.Check{Name: name, Detail: detail, Hard: true})
	}
	if p.FindingID != nil {
		f, err := g.s.st.Finding(ctx, *p.FindingID)
		switch {
		case err != nil:
			block("finding is active", "could not be read: "+err.Error())
		case f == nil:
			block("finding is active", "the finding no longer exists")
		case f.State == domain.FindingOpen || f.State == domain.FindingAcknowledged:
		case f.State == domain.FindingSnoozed && p.Origin == action.OriginManual:
		default:
			block("finding is active", "the finding is now "+string(f.State))
		}
	}
	if rs, err := g.s.st.CheckRestore(ctx); err != nil || rs.Suspected {
		detail := rs.Reason
		if err != nil {
			detail = err.Error()
		}
		block("state matches the action journal", detail)
	}
	pol, err := g.s.cfg.Resolve(p.Scope)
	if err != nil {
		block("policy", "could not be resolved: "+err.Error())
		return out
	}
	if pol.Revision != p.PolicyRevision {
		block("policy unchanged", fmt.Sprintf("the effective policy changed since the preview (revision %s, now %s)", p.PolicyRevision, pol.Revision))
	}
	if c := action.PolicyCheck(pol, policy.Workflow(p.Workflow), p.Origin); !c.Satisfied {
		out = append(out, c)
	}
	return out
}
