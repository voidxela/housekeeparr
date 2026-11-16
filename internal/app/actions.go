package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
		ConfigRevision: s.cfg.Revision, Tested: action.Tested, Now: s.now(), Limits: s.cfg.Limits()}
	if f.Subject.ClientID != "" && f.Subject.NativeID != "" {
		if in.Transfer, err = s.st.Transfer(ctx, f.Subject.ClientID, f.Subject.NativeID); err != nil {
			return nil, err
		}
		if in.Queue, err = s.st.QueueItems(ctx, store.QueueFilter{ClientID: f.Subject.ClientID, NativeID: f.Subject.NativeID}); err != nil {
			return nil, err
		}
	}
	var latest *domain.Transfer
	if in.Transfer != nil {
		latest = &in.Transfer.Latest
	}
	sub := policy.TransferSubject(f.Subject.ClientID, f.Subject.NativeID, latest, presentAssociations(in.Queue))
	conns, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
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
	if in.Policy, in.Scope, err = s.cfg.ResolveTransfer(sub); err != nil {
		return nil, err
	}
	out.Policy = in.Policy
	in.Owners = s.cfg.ManagersFor(f.Subject.ClientID)
	if in.Retained, err = s.st.RetainedUsage(ctx); err != nil {
		return nil, err
	}
	if in.Locks, err = s.st.HeldLocks(ctx); err != nil {
		return nil, err
	}
	if in.Cooldowns, err = s.st.ActiveCooldowns(ctx, in.Now); err != nil {
		return nil, err
	}
	p, np := action.Plan(in)
	if np != nil {
		out.NoAction = np.Reason
		return out, nil
	}
	usage := map[string]store.BudgetUsage{}
	for _, b := range p.Budgets {
		u, err := s.st.BudgetUsage(ctx, b.Budget(), b.Units, in.Now)
		if err != nil {
			return nil, err
		}
		usage[b.Scope] = u
	}
	p.CheckBudgets(usage)
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
	s.execMu.Lock()
	defer s.execMu.Unlock()
	// Settle anything a crash interrupted before planning new work, so a
	// half-finished action holds its locks as uncertain rather than as
	// executing forever. Holding execMu, no action of this process is in
	// flight.
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

// AttemptHistory lists recorded acquisition attempts and resets whose
// scope starts with prefix ("" for all), newest first.
func (s *Service) AttemptHistory(ctx context.Context, prefix string, limit int) ([]store.Attempt, []store.BudgetReset, error) {
	return s.st.Attempts(ctx, prefix, limit)
}

// ResetAttempts allows further recovery attempts for one episode or movie
// ("<manager-id>|episode:<id>" or "<manager-id>|movie:<id>") after an
// operator has reviewed why earlier replacements did not resolve it. The
// earlier attempts remain recorded; nothing is sent.
func (s *Service) ResetAttempts(ctx context.Context, ref string, actor Actor, note string) error {
	mid, key, ok := strings.Cut(ref, "|")
	if _, _, valid := domain.ParseContentKey(key); !ok || !valid || mid == "" {
		return fmt.Errorf("content must be \"<manager-id>|episode:<id>\" or \"<manager-id>|movie:<id>\"")
	}
	if !s.cfg.HasManager(mid) {
		return fmt.Errorf("unknown manager connection %q", mid)
	}
	if strings.TrimSpace(note) == "" {
		return fmt.Errorf("a note describing what you reviewed is required")
	}
	scope := action.AttemptScope(mid, key)
	now := s.now()
	return s.st.Write(ctx, func(tx *store.Tx) error {
		if err := tx.ResetBudget(scope, string(actor), note, now); err != nil {
			return err
		}
		return tx.Audit(now, string(actor), "attempts.reset", ref+": "+note)
	})
}

// Retained lists transfers recovery stopped and kept with their data.
func (s *Service) Retained(ctx context.Context, all bool) ([]store.RetainedTransfer, store.RetainedUsage, error) {
	list, err := s.st.RetainedTransfers(ctx, all, 0)
	if err != nil {
		return nil, store.RetainedUsage{}, err
	}
	u, err := s.st.RetainedUsage(ctx)
	return list, u, err
}

// Limits returns the effective acquisition and retention limits.
func (s *Service) Limits() policy.Limits { return s.cfg.Limits() }

// presentAssociations returns the present queue rows as policy associations.
func presentAssociations(rows []store.QueueRecord) []policy.Association {
	var out []policy.Association
	for _, q := range rows {
		if q.Present {
			out = append(out, policy.Association{ManagerID: q.ManagerID, Item: q.Latest})
		}
	}
	return out
}
