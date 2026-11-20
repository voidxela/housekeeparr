// Package action is the action coordinator (SPEC §§7, 10, 13.1): the one
// path through which both automatic and manual operational writes pass.
//
// A Proposal describes an intended workflow with its evidence, effective
// policy, prerequisites, exact operations, and side effects; the same value
// is the preview shown by the CLI and the web interface. Execute re-checks
// every prerequisite, re-reads decision-relevant external state and
// compares it with the plan (Revalidate), journals the intent with its
// locks and budget reservations before any external write, commits each
// step as dispatching before sending it, and verifies each step's
// postcondition by observation. Every later step gets its own fresh gate
// and live checks; a divergence holds the partial workflow for review. A request whose outcome cannot be
// established is never re-sent: the intent becomes uncertain and keeps its
// locks until an operator reviews it. Reconcile performs the same
// observation-only settlement at startup for anything a crash interrupted.
//
// The coordinator never talks to an application directly; it uses an
// Executor (internal/workflow). A workflow is eligible only for the exact
// manager/client versions recorded in Tested.
package action

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Origin says who asked for an action. Manual actions use exactly the same
// checks as automatic ones; they are not a bypass.
type Origin string

const (
	OriginAutomatic Origin = "automatic"
	OriginManual    Origin = "manual"
)

// Check is one prerequisite with its evaluation.
type Check struct {
	Name      string `json:"name"`
	Satisfied bool   `json:"satisfied"`
	Detail    string `json:"detail"`
	// Hard checks are safety prerequisites that no rule, override, or
	// manual request can waive.
	Hard bool `json:"hard"`
	// Code classifies an unsatisfied check for automation decisions and
	// explanations; "" is an ordinary prerequisite.
	Code CheckCode `json:"code,omitempty"`
	// Until is when an unsatisfied check clears by itself (an allowance or
	// cooldown expiring); nil when it does not.
	Until *time.Time `json:"until,omitempty"`
}

// CheckCode is a stable reason code for a prerequisite.
type CheckCode string

const (
	CodePolicy   CheckCode = "policy"   // mode, emergency stop, exclusion or workflow permission
	CodePaused   CheckCode = "paused"   // an operator paused the workflow
	CodeCapacity CheckCode = "capacity" // a retained count or byte ceiling, or room for this transfer
	CodeCooldown CheckCode = "cooldown" // a success cooldown on a lock subject
	CodePacing   CheckCode = "pacing"   // an acquisition pacing allowance
	// CodeAttempts is a per-content attempt allowance; with Until it
	// returns by itself (rolling window), without it only a reset helps.
	CodeAttempts CheckCode = "attempts"
	CodeLock     CheckCode = "lock" // another action holds a lock subject
)

// Operation is one intended external operation. Request must carry every
// consequential flag explicitly; Expect is the postcondition to verify.
type Operation struct {
	Op          string          `json:"op"`
	Target      json.RawMessage `json:"target"`
	Request     json.RawMessage `json:"request"`
	Expect      json.RawMessage `json:"expect"`
	Description string          `json:"description"`
}

// Proposal is an evaluated, previewable action.
type Proposal struct {
	Workflow string `json:"workflow"`
	// Permission is the workflow whose automation permission and pause
	// govern the proposal when it differs from Workflow (a partial pack's
	// targeted search runs the missing_search mechanics under partial_pack).
	Permission     string          `json:"permission,omitempty"`
	Origin         Origin          `json:"origin"`
	FindingID      *int64          `json:"finding_id,omitempty"`
	Subject        json.RawMessage `json:"subject"`
	Locks          []string        `json:"locks"`
	Budgets        []BudgetClaim   `json:"budgets,omitempty"`
	Operations     []Operation     `json:"operations"`
	Effects        []string        `json:"effects"`
	SideEffects    []string        `json:"side_effects"`
	Prerequisites  []Check         `json:"prerequisites"`
	Fingerprint    string          `json:"fingerprint"`
	PolicyRevision string          `json:"policy_revision"`
	ConfigRevision string          `json:"config_revision"`
	Evidence       json.RawMessage `json:"evidence"`
	// Planned is the external state the proposal was planned against;
	// it is re-verified before every write (Revalidate).
	Planned Planned `json:"planned"`
	// LiveChecks describes what is re-read before every write.
	LiveChecks []string `json:"live_checks,omitempty"`
	// Scope is the policy subject; the gate re-resolves policy for it.
	Scope policy.Subject `json:"scope"`
	// UnknownPrivacy records that the transfer's privacy was unknown and the
	// scope allows the workflow then (policy.AllowsUnknownPrivacy); revalidation
	// then accepts privacy that is still unknown, never known private.
	UnknownPrivacy bool `json:"unknown_privacy,omitempty"`
	// PrivateMode records that the transfer was private or of unknown
	// privacy and which private_recovery mode permitted the plan;
	// revalidation then does not require a public transfer.
	PrivateMode policy.PrivateMode `json:"private_mode,omitempty"`
	// Cooldown applied to every lock subject after success.
	Cooldown  time.Duration `json:"cooldown,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	ExpiresAt time.Time     `json:"expires_at"`
}

// Blockers returns the unsatisfied prerequisites.
func (p Proposal) Blockers() []Check {
	var out []Check
	for _, c := range p.Prerequisites {
		if !c.Satisfied {
			out = append(out, c)
		}
	}
	return out
}

// Eligible reports whether every prerequisite is satisfied.
func (p Proposal) Eligible() bool { return len(p.Blockers()) == 0 }

// Key is the local idempotency key: the same workflow on the same subject
// for the same observed state yields the same key.
func (p Proposal) Key() string {
	return digest(fmt.Sprintf("%s|%s|%s", p.Workflow, p.Subject, p.Fingerprint))[:24]
}

func digest(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Verdict is the result of observing a step's postcondition.
type Verdict string

const (
	Holds    Verdict = "holds"
	NotHolds Verdict = "not_holds"
	Unknown  Verdict = "unknown"
)

// Observation is evidence about a step's postcondition.
type Observation struct {
	Verdict  Verdict         `json:"verdict"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
	Detail   string          `json:"detail,omitempty"`
}

// Executor performs and observes operations for the coordinator. Dispatch
// must return *adapter.Error for remote failures. Live and Observe are
// read-only.
type Executor interface {
	// Live re-reads the decision-relevant external state for p, for
	// Revalidate. It must not write.
	Live(ctx context.Context, p Proposal) (Live, error)
	// Baseline reads, immediately before a step is dispatched, the
	// external evidence its verification compares against (for example
	// existing matching blocklist entries), so only evidence that is new
	// relative to it is attributed to this request. It may return nil.
	Baseline(ctx context.Context, step store.Step) (json.RawMessage, error)
	Dispatch(ctx context.Context, step store.Step) (json.RawMessage, error)
	Observe(ctx context.Context, step store.Step) (Observation, error)
}

// OutcomeObserver is an optional Executor capability: right after a
// verified recovery it observes what later polling would likely miss, the
// manager's replacement search (managers drop finished commands from their
// list some time after they end; the retention is not established). It must not write externally and must
// return within a short bound; it only updates o.Search.
type OutcomeObserver interface {
	// now is the coordinator's clock, against which the outcome's times
	// were recorded.
	ObserveOutcome(ctx context.Context, o *store.RecoveryOutcome, now func() time.Time) (changed bool, err error)
}

// Gate supplies the global conditions evaluated for every execution.
type Gate interface {
	// Blockers returns unsatisfied prerequisites that are not external
	// state: mode, emergency stop, exclusions, and workflow permission as
	// currently configured for p's scope. It is evaluated before every
	// write, not only the first.
	Blockers(ctx context.Context, p Proposal) []Check
}

// Coordinator executes proposals durably.
type Coordinator struct {
	Store    *store.Store
	Executor Executor
	Gate     Gate
	Now      func() time.Time
	Actor    string
	// LegacyCooldown is the success cooldown reconciliation applies to an
	// intent journaled before cooldowns were recorded with it (the
	// currently configured success_cooldown).
	LegacyCooldown time.Duration
}

// Errors returned by Execute. Each leaves external state untouched unless
// stated otherwise.
var (
	ErrNotEligible = errors.New("action is not eligible")
	ErrExpired     = errors.New("preview expired; recalculate it")
	ErrStale       = errors.New("decision-relevant state changed since the preview")
	ErrConflict    = errors.New("a conflicting action holds a required lock")
	ErrRestoreHold = errors.New("writes are held: the state database may predate actions already sent")
	// ErrJournal matches every failure to persist the action journal
	// (errors.Is). Whether a request may already have been sent is carried
	// by *JournalError; a bare ErrJournal (a read-only store) sent nothing.
	ErrJournal = errors.New("the action journal could not be written")
)

// JournalError is a failure to persist the action journal. It is an
// integrity failure, not a scoped refusal: callers stop further acquisition
// work until reconciliation has settled the journal again.
type JournalError struct {
	// IntentID is the intent concerned, 0 if none was journaled.
	IntentID int64
	// Sent reports that a request was dispatched before the failure, so it
	// may have taken effect. Such an intent is settled by observation at
	// the next reconciliation and never re-sent.
	Sent bool
	// Stage names the journal write that failed.
	Stage string
	Err   error
}

func (e *JournalError) Error() string {
	msg := ErrJournal.Error()
	if e.Stage != "" {
		msg += " (" + e.Stage + ")"
	}
	if e.Sent {
		msg += "; a request was already sent and may have taken effect: it is settled by observation at the next reconciliation and never re-sent"
	} else {
		msg += "; no request was sent"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *JournalError) Is(target error) bool { return target == ErrJournal }
func (e *JournalError) Unwrap() error        { return e.Err }

// MaybeSent reports whether err is a journal failure after a request was
// dispatched, whose effect is therefore unknown until reconciliation.
func MaybeSent(err error) bool {
	var je *JournalError
	return errors.As(err, &je) && je.Sent
}

// RefusedError carries the checks that prevented execution.
type RefusedError struct {
	Err    error
	Checks []Check
}

func (e *RefusedError) Error() string {
	var parts []string
	for _, c := range e.Checks {
		parts = append(parts, c.Name+": "+c.Detail)
	}
	if len(parts) == 0 {
		return e.Err.Error()
	}
	return e.Err.Error() + " (" + strings.Join(parts, "; ") + ")"
}

func (e *RefusedError) Unwrap() error { return e.Err }

// Result is what Execute did.
type Result struct {
	IntentID int64             `json:"intent_id"`
	State    store.IntentState `json:"state"`
	Detail   string            `json:"detail"`
}

func (c *Coordinator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// definiteFailure reports whether err proves the request was refused
// without effect. Anything else (timeouts, transport failures, malformed
// responses to an accepted request) is treated as an uncertain outcome.
func definiteFailure(err error) bool {
	var ae *adapter.Error
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Class {
	case adapter.ErrUnauthorized, adapter.ErrUnsupported, adapter.ErrRateLimited:
		return true
	case adapter.ErrPermanent:
		return ae.HTTPStatus >= 400 && ae.HTTPStatus < 500
	}
	return false
}

// Execute runs an eligible proposal. Before any external write it verifies
// the gate and prerequisites, the restore marker, the preview's expiry,
// local locks, and a fresh live read; then it journals the intent, locks,
// and budgets in one transaction.
func (c *Coordinator) Execute(ctx context.Context, p Proposal) (Result, error) {
	now := c.now()
	if !p.ExpiresAt.IsZero() && now.After(p.ExpiresAt) {
		return Result{}, ErrExpired
	}
	checks := append(p.Blockers(), c.gateBlockers(ctx, p)...)
	if len(checks) > 0 {
		return Result{}, &RefusedError{Err: ErrNotEligible, Checks: checks}
	}
	if c.Store.ReadOnly() {
		return Result{}, &RefusedError{Err: ErrJournal, Checks: []Check{{Name: "writable state store", Detail: "the state store is open read-only", Hard: true}}}
	}
	rs, err := c.Store.CheckRestore(ctx)
	if err != nil {
		return Result{}, &JournalError{Stage: "restore check", Err: err}
	}
	if rs.Suspected {
		return Result{}, &RefusedError{Err: ErrRestoreHold, Checks: []Check{{Name: "state matches the action journal", Detail: rs.Reason, Hard: true}}}
	}
	// Local conflicts are cheaper to detect than external state; the
	// journal transaction below remains the authoritative lock check.
	for _, l := range p.Locks {
		if h, err := c.Store.LockHolder(ctx, l); err != nil {
			return Result{}, &JournalError{Stage: "lock check", Err: err}
		} else if h != 0 {
			return Result{}, &RefusedError{Err: ErrConflict, Checks: []Check{{Name: "exclusive access", Detail: (&store.LockHeldError{Subject: l, Holder: h}).Error(), Hard: true}}}
		}
	}
	live, err := c.Executor.Live(ctx, p)
	if err != nil {
		return Result{}, fmt.Errorf("re-read before write failed; nothing was sent: %w", err)
	}
	if bad := unsatisfied(Revalidate(p, 0, live)); len(bad) > 0 {
		return Result{}, &RefusedError{Err: ErrStale, Checks: bad}
	}

	in := store.Intent{Key: p.Key(), Workflow: p.Workflow, Origin: string(p.Origin), FindingID: p.FindingID, Subject: p.Subject,
		PolicyRevision: p.PolicyRevision, ConfigRevision: p.ConfigRevision, InstanceID: c.Store.InstanceID(), Fingerprint: p.Fingerprint,
		Evidence: p.Evidence, Locks: p.Locks}
	cooldown := p.Cooldown.Milliseconds()
	plan, _ := json.Marshal(journaledPlan{Operations: p.Operations, Effects: p.Effects, SideEffects: p.SideEffects,
		CooldownMS: &cooldown, Planned: &p.Planned})
	in.Plan = plan
	for _, op := range p.Operations {
		in.Steps = append(in.Steps, store.Step{Op: op.Op, Target: op.Target, Request: op.Request, Expect: op.Expect})
	}
	var id int64
	err = c.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		id, err = tx.CreateIntent(in, now)
		if err != nil {
			return err
		}
		for _, b := range p.Budgets {
			if err := tx.ReserveBudget(id, b.Budget(), b.Units, now); err != nil {
				var ex *store.BudgetExhaustedError
				if errors.As(err, &ex) {
					return &RefusedError{Err: ErrNotEligible, Checks: []Check{BudgetCheck(b, ex.Usage)}}
				}
				return err
			}
		}
		return tx.Audit(now, c.actor(), "action.reserved", fmt.Sprintf("#%d %s (%s)", id, p.Workflow, p.Origin))
	})
	var held *store.LockHeldError
	var refused *RefusedError
	switch {
	case errors.As(err, &refused):
		return Result{}, refused
	case errors.As(err, &held):
		return Result{}, &RefusedError{Err: ErrConflict, Checks: []Check{{Name: "exclusive access", Detail: held.Error(), Hard: true}}}
	case errors.Is(err, store.ErrDuplicateIntent):
		return Result{}, &RefusedError{Err: ErrConflict, Checks: []Check{{Name: "not already attempted", Detail: "this action was already journaled for the same observed state", Hard: true}}}
	case err != nil:
		return Result{}, &JournalError{Stage: "reservation", Err: err}
	}
	if err := c.Store.NoteJournalHighWater(id); err != nil {
		jerr := &JournalError{IntentID: id, Stage: "journal high-water marker", Err: err}
		why := "journal marker could not be written; cancelled before any request"
		if cerr := c.conclude(context.WithoutCancel(ctx), id, nil, store.IntentCancelled, why, true); cerr != nil {
			// Still reserved: the next reconciliation cancels it.
			return Result{IntentID: id, State: store.IntentReserved, Detail: why}, errors.Join(jerr, cerr)
		}
		return Result{IntentID: id, State: store.IntentCancelled, Detail: why}, jerr
	}
	return c.run(ctx, id, p)
}

func (c *Coordinator) gateBlockers(ctx context.Context, p Proposal) []Check {
	if c.Gate == nil {
		return nil
	}
	return unsatisfied(c.Gate.Blockers(ctx, p))
}

func unsatisfied(cs []Check) []Check {
	var out []Check
	for _, c := range cs {
		if !c.Satisfied {
			out = append(out, c)
		}
	}
	return out
}

// recheck repeats the gate and live checks before operation i > 0. It
// returns why the remaining operations must not be sent, or "".
func (c *Coordinator) recheck(ctx context.Context, p Proposal, i int) string {
	bad := c.gateBlockers(ctx, p)
	live, err := c.Executor.Live(ctx, p)
	if err != nil {
		bad = append(bad, Check{Name: "live state", Detail: "could not be re-read: " + err.Error()})
	} else {
		bad = append(bad, unsatisfied(Revalidate(p, i, live))...)
	}
	var parts []string
	for _, b := range bad {
		parts = append(parts, b.Name+": "+b.Detail)
	}
	return strings.Join(parts, "; ")
}

func (c *Coordinator) actor() string {
	if c.Actor != "" {
		return c.Actor
	}
	return "housekeeparr"
}

// conclude records an intent's terminal state in one transaction with
// the steps that will not run (skipped). release returns its budget
// reservations: only for an intent none of whose requests can have had an
// external effect (never sent, or definitely refused); the ledger keeps
// the released rows as history.
func (c *Coordinator) conclude(ctx context.Context, id int64, skip []store.Step, st store.IntentState, why string, release bool) error {
	now := c.now()
	return c.Store.Write(ctx, func(tx *store.Tx) error {
		for _, s := range skip {
			if err := tx.FinishStep(id, s.Seq, store.StepSkipped, nil, "", now); err != nil {
				return err
			}
		}
		if err := tx.SetIntentState(id, st, why, now); err != nil {
			return err
		}
		detail := fmt.Sprintf("#%d: %s", id, why)
		if release {
			if err := tx.ReleaseBudget(id, why, now); err != nil {
				return err
			}
			detail += "; budget reservations returned"
		}
		return tx.Audit(now, c.actor(), "action."+string(st), detail)
	})
}

// end concludes the intent and reports it. If the journal write fails,
// the result keeps the last durable state and a *JournalError says whether
// a request may have been sent; reconciliation settles it later.
func (c *Coordinator) end(ctx context.Context, id int64, skip []store.Step, st store.IntentState, why string, release bool,
	durable store.IntentState, sent bool, cause error) (Result, error) {
	if err := c.conclude(ctx, id, skip, st, why, release); err != nil {
		return Result{IntentID: id, State: durable, Detail: why},
			errors.Join(&JournalError{IntentID: id, Sent: sent, Stage: "recording the " + string(st) + " outcome", Err: err}, cause)
	}
	return Result{IntentID: id, State: st, Detail: why}, cause
}

// run dispatches the journaled steps in order.
func (c *Coordinator) run(ctx context.Context, id int64, p Proposal) (Result, error) {
	// Journal writes and postcondition checks must not be abandoned once a
	// request may have been sent: a stop (cancelled ctx) only prevents
	// further dispatches, and graceful shutdown persists the current step.
	jctx := context.WithoutCancel(ctx)
	in, err := c.Store.Intent(jctx, id)
	if err != nil {
		return Result{IntentID: id, State: store.IntentReserved}, &JournalError{IntentID: id, Stage: "reading the journaled intent", Err: err}
	}
	// durable is the intent state last committed.
	durable := store.IntentReserved
	for i, st := range in.Steps {
		rest := in.Steps[i:]
		// Nothing was sent before the first step; afterwards earlier steps
		// took effect (they were verified), so a stop holds the partial
		// workflow for review.
		stop := func(why string, cause error) (Result, error) {
			if i == 0 {
				return c.end(jctx, id, rest, store.IntentCancelled, why+"; nothing was sent", true, durable, false, cause)
			}
			return c.end(jctx, id, rest, store.IntentUncertain, why+"; earlier steps took effect, review required", false, durable, true, cause)
		}
		if err := ctx.Err(); err != nil {
			return stop(fmt.Sprintf("stopped before step %d: %v", st.Seq, err), err)
		}
		// The first step was checked immediately before the intent was
		// journaled. Every later write gets its own fresh checks; a
		// divergence holds the partial workflow for review.
		if i > 0 {
			if why := c.recheck(ctx, p, i); why != "" {
				return stop(fmt.Sprintf("held before step %d (%s): %s", st.Seq, st.Op, why), nil)
			}
		}
		base, berr := c.Executor.Baseline(ctx, st)
		if berr != nil {
			return stop(fmt.Sprintf("the evidence baseline for step %d (%s) could not be read: %v", st.Seq, st.Op, berr), nil)
		}
		// Commit "dispatching", with the baseline, before sending. If this
		// write fails nothing is sent.
		now := c.now()
		err := c.Store.Write(jctx, func(tx *store.Tx) error {
			if i == 0 {
				if err := tx.SetIntentState(id, store.IntentExecuting, "", now); err != nil {
					return err
				}
			}
			return tx.BeginStep(id, st.Seq, now, base)
		})
		if err != nil {
			jerr := &JournalError{IntentID: id, Sent: i > 0, Stage: fmt.Sprintf("recording step %d as dispatching", st.Seq), Err: err}
			if i == 0 {
				// Best effort: if this fails too, reconciliation cancels the
				// still-reserved intent.
				res, cerr := c.end(jctx, id, rest, store.IntentCancelled, "journal write failed before the first request; nothing was sent", true, durable, false, nil)
				return res, errors.Join(jerr, cerr)
			}
			// Earlier steps took effect; leave the intent for reconciliation.
			return Result{IntentID: id, State: durable}, jerr
		}
		durable = store.IntentExecuting
		st.State, st.DispatchedAt, st.Attempts, st.Baseline = store.StepDispatching, &now, st.Attempts+1, base
		out, derr := c.Executor.Dispatch(ctx, st)
		state, detail, outcome := c.settle(jctx, st, out, derr)
		if err := c.Store.Write(jctx, func(tx *store.Tx) error {
			if err := tx.FinishStep(id, st.Seq, state, outcome, detail, c.now()); err != nil {
				return err
			}
			if err := trackMissingSearch(tx, st, state); err != nil {
				return err
			}
			if state == store.StepSucceeded {
				return recordVerifiedStep(tx, id, in.Subject, st, &p.Planned, c.now())
			}
			return nil
		}); err != nil {
			// The outcome is not durable; reconciliation will observe this
			// step, which is still recorded as dispatching.
			return Result{IntentID: id, State: durable, Detail: detail},
				&JournalError{IntentID: id, Sent: true, Stage: fmt.Sprintf("recording the outcome of step %d (%s)", st.Seq, st.Op), Err: err}
		}
		switch state {
		case store.StepFailed:
			why := fmt.Sprintf("step %d (%s) failed: %s", st.Seq, st.Op, detail)
			if i > 0 {
				// Earlier steps took effect: the workflow is partial.
				return c.end(jctx, id, in.Steps[i+1:], store.IntentUncertain, why+"; earlier steps took effect, review required", false, durable, true, nil)
			}
			// Only a definite refusal fails a step (see settle), so the
			// first request had no effect.
			return c.end(jctx, id, in.Steps[i+1:], store.IntentFailed, why+"; refused before any effect", true, durable, true, nil)
		case store.StepUncertain:
			why := fmt.Sprintf("step %d (%s) outcome could not be established: %s; it will not be retried automatically", st.Seq, st.Op, detail)
			return c.end(jctx, id, in.Steps[i+1:], store.IntentUncertain, why, false, durable, true, nil)
		}
	}
	now := c.now()
	err = c.Store.Write(jctx, func(tx *store.Tx) error {
		return c.succeed(tx, id, p.Workflow, p.Locks, completion{cooldown: p.Cooldown, recorded: true}, "every step verified", now, now)
	})
	if err != nil {
		return Result{IntentID: id, State: durable, Detail: "every step verified; completion not recorded"},
			&JournalError{IntentID: id, Sent: true, Stage: "recording completion", Err: err}
	}
	c.observeOutcome(ctx, jctx, id)
	return Result{IntentID: id, State: store.IntentSucceeded, Detail: "every step verified"}, nil
}

// observeOutcome records what the executor can observe of the replacement
// search right after a verified recovery. Failure to observe changes
// nothing: the outcome stays unobserved and the engine keeps looking.
// External reads use ctx (cancelled on shutdown); the local record uses
// jctx.
func (c *Coordinator) observeOutcome(ctx, jctx context.Context, id int64) {
	obs, ok := c.Executor.(OutcomeObserver)
	if !ok {
		return
	}
	os, err := c.Store.Outcomes(jctx, []int64{id})
	if err != nil {
		return
	}
	o, ok := os[id]
	if !ok {
		return
	}
	if changed, err := obs.ObserveOutcome(ctx, &o, c.now); err != nil || !changed {
		return
	}
	_ = c.Store.Write(jctx, func(tx *store.Tx) error { return tx.SaveSearchOutcome(id, o.Search) })
}

// settle classifies a dispatch result and verifies the postcondition.
func (c *Coordinator) settle(ctx context.Context, st store.Step, out json.RawMessage, derr error) (store.StepState, string, json.RawMessage) {
	if derr != nil && definiteFailure(derr) {
		return store.StepFailed, derr.Error(), out
	}
	if st.Op == OpSearch {
		// The manager's receipt identifies the command the verifier should
		// observe, even if another actor submits a matching search.
		st.Outcome = out
	}
	obs, oerr := c.Executor.Observe(ctx, st)
	evidence, _ := json.Marshal(struct {
		Response    json.RawMessage `json:"response,omitempty"`
		Observation Observation     `json:"observation"`
	}{out, obs})
	switch {
	case oerr != nil:
		msg := "postcondition could not be observed: " + oerr.Error()
		if derr != nil {
			msg = derr.Error() + "; " + msg
		}
		return store.StepUncertain, msg, evidence
	case obs.Verdict == Holds:
		return store.StepSucceeded, "", evidence
	case obs.Verdict == NotHolds && derr == nil:
		// Accepted, but the effect was not observed within the observation
		// window. That does not prove it never takes effect (an accepted
		// acquisition may act later), so it is held, never made retryable.
		return store.StepUncertain, "accepted, but the expected effect was not observed: " + obs.Detail +
			"; a delayed effect cannot be ruled out", evidence
	default:
		msg := "expected effect not established: " + obs.Detail
		if derr != nil {
			msg = derr.Error() + "; " + msg
		}
		return store.StepUncertain, msg, evidence
	}
}

// ReconcileReport summarises startup reconciliation.
type ReconcileReport struct {
	RestoreHold *store.RestoreStatus `json:"restore_hold,omitempty"`
	Cancelled   []int64              `json:"cancelled,omitempty"`
	Settled     []int64              `json:"settled,omitempty"`
	Uncertain   []int64              `json:"uncertain,omitempty"`
}

// Reconcile settles intents a crash or shutdown interrupted. It observes;
// it never dispatches. Reserved intents with nothing sent are cancelled
// (never replayed). A step recorded as dispatching is observed: if its
// postcondition holds it is recorded as succeeded, otherwise uncertain. An
// intent interrupted part-way is held as uncertain for review rather than
// continued, because the state it was planned against may have changed.
// With a restore suspected, nothing is changed and the hold is reported.
func (c *Coordinator) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var rep ReconcileReport
	rs, err := c.Store.CheckRestore(ctx)
	if err != nil {
		return rep, err
	}
	if rs.Suspected {
		rep.RestoreHold = &rs
		return rep, nil
	}
	active, err := c.Store.Intents(ctx, store.IntentFilter{States: []store.IntentState{store.IntentReserved, store.IntentExecuting, store.IntentReconciling}})
	if err != nil {
		return rep, err
	}
	sort.Slice(active, func(i, j int) bool { return active[i].ID < active[j].ID })
	for _, in := range active {
		dispatched := false
		for _, s := range in.Steps {
			dispatched = dispatched || (s.State != store.StepPending && s.State != store.StepSkipped)
		}
		if !dispatched {
			if err := c.conclude(ctx, in.ID, pendingSteps(in.Steps), store.IntentCancelled, "interrupted before any request was sent; not replayed", true); err != nil {
				return rep, err
			}
			rep.Cancelled = append(rep.Cancelled, in.ID)
			continue
		}
		// The first request was definitely refused (its failure is durable)
		// and nothing else was sent: it had no effect, as when the outcome
		// is recorded without interruption.
		if refusedFirst(in.Steps) {
			if err := c.conclude(ctx, in.ID, pendingSteps(in.Steps), store.IntentFailed,
				fmt.Sprintf("step %d (%s) was refused before any effect; settled after interruption", in.Steps[0].Seq, in.Steps[0].Op), true); err != nil {
				return rep, err
			}
			rep.Settled = append(rep.Settled, in.ID)
			continue
		}
		if in.State != store.IntentReconciling {
			if err := c.Store.Write(ctx, func(tx *store.Tx) error {
				return tx.SetIntentState(in.ID, store.IntentReconciling, "", c.now())
			}); err != nil {
				return rep, err
			}
		}
		allDone, uncertain := true, false
		var notes []string
		for _, s := range in.Steps {
			switch s.State {
			case store.StepDispatching, store.StepUncertain:
				obs, oerr := c.Executor.Observe(ctx, s)
				to, note := store.StepUncertain, ""
				switch {
				case oerr != nil:
					note = "could not observe: " + oerr.Error()
				case obs.Verdict == Holds:
					to = store.StepSucceeded
				default:
					note = "expected effect not established: " + obs.Detail
				}
				ev, _ := json.Marshal(obs)
				if err := c.Store.Write(ctx, func(tx *store.Tx) error {
					if err := tx.FinishStep(in.ID, s.Seq, to, ev, note, c.now()); err != nil {
						return err
					}
					if err := trackMissingSearch(tx, s, to); err != nil {
						return err
					}
					if to == store.StepSucceeded {
						return recordVerifiedStep(tx, in.ID, in.Subject, s, plannedOf(in), c.now())
					}
					return nil
				}); err != nil {
					return rep, err
				}
				if to == store.StepUncertain {
					uncertain = true
					notes = append(notes, fmt.Sprintf("step %d (%s) %s", s.Seq, s.Op, note))
				}
			case store.StepPending, store.StepSkipped:
				allDone = false
				if s.State == store.StepSkipped {
					notes = append(notes, fmt.Sprintf("step %d (%s) had been skipped", s.Seq, s.Op))
				}
			case store.StepFailed:
				uncertain = true
				notes = append(notes, fmt.Sprintf("step %d (%s) failed", s.Seq, s.Op))
			}
		}
		switch {
		case !uncertain && allDone:
			why := "verified by reconciliation after interruption"
			done := c.completionOf(in)
			if err := c.Store.Write(ctx, func(tx *store.Tx) error {
				now := c.now()
				if err := c.succeed(tx, in.ID, in.Workflow, in.Locks, done, why, lastDispatch(in.Steps, now), now); err != nil {
					return err
				}
				return trackOutcome(tx, in.ID, in.Subject, in.Steps, now)
			}); err != nil {
				return rep, err
			}
			rep.Settled = append(rep.Settled, in.ID)
		default:
			pend := pendingSteps(in.Steps)
			why := "interrupted"
			if len(pend) > 0 {
				why += fmt.Sprintf("; %d remaining step(s) were not run", len(pend))
			}
			if len(notes) > 0 {
				why += "; " + strings.Join(notes, "; ")
			}
			if err := c.conclude(ctx, in.ID, pend, store.IntentUncertain, why+"; review required", false); err != nil {
				return rep, err
			}
			rep.Uncertain = append(rep.Uncertain, in.ID)
		}
	}
	return rep, nil
}

// journaledPlan is the intent's plan as journaled: the operations and
// effects shown to the operator, and the facts needed to finish the action
// identically whether it completes normally or is settled by
// reconciliation after an interruption.
type journaledPlan struct {
	Operations  []Operation `json:"operations"`
	Effects     []string    `json:"effects"`
	SideEffects []string    `json:"side_effects"`
	// CooldownMS is the success cooldown applied to every lock subject; nil
	// for intents journaled before it was recorded.
	CooldownMS *int64 `json:"cooldown_ms,omitempty"`
	// Planned is the external state planned against (retained sizes).
	Planned *Planned `json:"planned,omitempty"`
}

func planOf(in store.Intent) journaledPlan {
	var jp journaledPlan
	_ = json.Unmarshal(in.Plan, &jp)
	return jp
}

func plannedOf(in store.Intent) *Planned { return planOf(in).Planned }

// completion is how a succeeded intent is finished.
type completion struct {
	cooldown time.Duration
	// recorded: the cooldown was journaled with the intent; otherwise it
	// is the legacy fallback and the reason says so.
	recorded bool
}

// cooldownWorkflows set a success cooldown (recovery plans; searches and
// pack clearances intentionally do not).
var cooldownWorkflows = map[string]bool{"no_progress_recovery": true, "metadata_recovery": true, "preserved_data_failure": true}

// completionOf recovers an intent's completion facts. An intent journaled
// before cooldowns were recorded takes the currently configured cooldown
// (LegacyCooldown) when its workflow sets one.
func (c *Coordinator) completionOf(in store.Intent) completion {
	if ms := planOf(in).CooldownMS; ms != nil {
		return completion{cooldown: time.Duration(*ms) * time.Millisecond, recorded: true}
	}
	if cooldownWorkflows[in.Workflow] {
		return completion{cooldown: c.LegacyCooldown}
	}
	return completion{recorded: true}
}

// succeed is the one success finalization, for normal completion and for
// reconciliation: the intent's state, a cooldown from since on each of its
// original lock subjects, and the audit record, in the caller's
// transaction.
func (c *Coordinator) succeed(tx *store.Tx, id int64, workflow string, locks []string, done completion, why string, since, now time.Time) error {
	if err := tx.SetIntentState(id, store.IntentSucceeded, why, now); err != nil {
		return err
	}
	if done.cooldown > 0 {
		reason := fmt.Sprintf("action #%d (%s)", id, workflow)
		if !done.recorded {
			reason = fmt.Sprintf("action #%d (%s; cooldown from current settings: the action predates recorded cooldowns)", id, workflow)
		}
		for _, l := range locks {
			if err := tx.SetCooldown(l, since, since.Add(done.cooldown), reason); err != nil {
				return err
			}
		}
	}
	return tx.Audit(now, c.actor(), "action.succeeded", fmt.Sprintf("#%d %s: %s", id, workflow, why))
}

// lastDispatch is when the last dispatched step was sent: the time the
// action's effect started, for a cooldown settled after interruption.
func lastDispatch(steps []store.Step, fallback time.Time) time.Time {
	var at *time.Time
	for _, s := range steps {
		if s.DispatchedAt != nil && (at == nil || s.DispatchedAt.After(*at)) {
			at = s.DispatchedAt
		}
	}
	if at == nil {
		return fallback
	}
	return *at
}

func pendingSteps(steps []store.Step) []store.Step {
	var out []store.Step
	for _, s := range steps {
		if s.State == store.StepPending {
			out = append(out, s)
		}
	}
	return out
}

// refusedFirst reports whether the first step definitely failed and no
// other step was dispatched.
func refusedFirst(steps []store.Step) bool {
	if len(steps) == 0 || steps[0].State != store.StepFailed {
		return false
	}
	for _, s := range steps[1:] {
		if s.State != store.StepPending && s.State != store.StepSkipped {
			return false
		}
	}
	return true
}

// Review resolves an uncertain intent after an operator has checked the
// external state. It sends nothing; it records the decision and releases
// the intent's locks.
func (c *Coordinator) Review(ctx context.Context, id int64, to store.IntentState, actor, note string) error {
	if to != store.IntentSucceeded && to != store.IntentFailed && to != store.IntentCancelled {
		return fmt.Errorf("review must resolve to succeeded, failed, or cancelled")
	}
	if strings.TrimSpace(note) == "" {
		return fmt.Errorf("a review note is required")
	}
	in, err := c.Store.Intent(ctx, id)
	if err != nil {
		return err
	}
	if in.State != store.IntentUncertain {
		return fmt.Errorf("action #%d is %s, not uncertain", id, in.State)
	}
	now := c.now()
	return c.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.SetIntentState(id, to, "reviewed by "+actor+": "+note, now); err != nil {
			return err
		}
		return tx.Audit(now, actor, "action.reviewed", fmt.Sprintf("#%d → %s: %s", id, to, note))
	})
}

// recordVerifiedStep commits effects of a verified operation even when a
// later step will be held or refused.
func recordVerifiedStep(tx *store.Tx, id int64, subject json.RawMessage, step store.Step, planned *Planned, now time.Time) error {
	if step.Op == OpPackIgnore {
		return trackPackFollowUps(tx, id, step, now)
	}
	if err := recordRetained(tx, id, subject, step, planned, now); err != nil {
		return err
	}
	return trackOutcome(tx, id, subject, []store.Step{step}, now)
}

// recordRetained records the transfer a verified step left in the client
// with its data (SPEC §7.1): once the manager has failed its grab the
// transfer is retained, running; a verified data-retaining stop then marks
// it stopped. A private torrent under keep_running, or one whose stop did
// not take effect, therefore stays counted as running. Sizes come from the
// plan when known; reconciliation after a crash has only the subject.
func recordRetained(tx *store.Tx, id int64, subject json.RawMessage, st store.Step, pl *Planned, now time.Time) error {
	var tg struct {
		ClientID string `json:"client_id"`
		NativeID string `json:"native_id"`
	}
	disp := store.RetainedStopped
	switch st.Op {
	case "client.stop":
		_ = json.Unmarshal(st.Target, &tg)
	case "manager.queue.fail":
		_ = json.Unmarshal(subject, &tg)
		disp = store.RetainedRunning
	default:
		return nil
	}
	if tg.ClientID == "" || tg.NativeID == "" {
		// The step outcome must be recorded regardless; executors always
		// target a transfer, so this only drops metadata for a malformed step.
		return nil
	}
	r := store.RetainedTransfer{ClientID: tg.ClientID, NativeID: tg.NativeID, IntentID: id, RetainedAt: now, Disposition: disp}
	if pl != nil && pl.ClientID == tg.ClientID && pl.NativeID == tg.NativeID {
		r.Name, r.ManagerID = pl.Name, pl.ManagerID
		r.Content = strings.Join(pl.Content, ",")
		// Unknown logical size stays unknown: a transfer planned before
		// its metadata arrived has no payload (verified zero), not a
		// known zero size.
		if v, ok := pl.Wanted.Get(); ok {
			r.WantedBytes = &v
		}
		if v, ok := pl.Verified.Get(); ok {
			r.VerifiedBytes = &v
		} else if pl.StateClass == domain.StateMetadata {
			zero := int64(0)
			r.VerifiedBytes = &zero
		}
		// Positive provenance: stopped before its metadata arrived.
		r.NoPayload = st.Op == "client.stop" && pl.StateClass == domain.StateMetadata
	}
	return tx.RecordRetained(r)
}

// trackOutcome starts observing the replacement after a recovery whose
// manager step failed a grab with the manager owning the redownload. The
// engine then records a replacement grab, its progress, and completion as
// separate outcomes.
func trackOutcome(tx *store.Tx, id int64, subject json.RawMessage, steps []store.Step, now time.Time) error {
	var sub struct {
		ClientID string `json:"client_id"`
	}
	_ = json.Unmarshal(subject, &sub)
	for _, st := range steps {
		if st.Op != "manager.queue.fail" {
			continue
		}
		var request struct {
			SkipRedownload *bool `json:"skipRedownload"`
		}
		if json.Unmarshal(st.Request, &request) != nil || request.SkipRedownload == nil || *request.SkipRedownload {
			continue
		}
		var tg struct {
			ManagerID  string  `json:"manager_id"`
			DownloadID string  `json:"download_id"`
			EpisodeID  int64   `json:"episode_id"`
			EpisodeIDs []int64 `json:"episode_ids"`
			MovieID    int64   `json:"movie_id"`
		}
		if err := json.Unmarshal(st.Target, &tg); err != nil || tg.ManagerID == "" {
			return nil
		}
		var content []string
		switch {
		case len(tg.EpisodeIDs) > 1:
			// A multi-episode release: every episode's replacement is
			// followed.
			for _, e := range tg.EpisodeIDs {
				content = append(content, fmt.Sprintf("episode:%d", e))
			}
		case tg.MovieID > 0:
			content = []string{fmt.Sprintf("movie:%d", tg.MovieID)}
		case tg.EpisodeID > 0:
			content = []string{fmt.Sprintf("episode:%d", tg.EpisodeID)}
		default:
			return nil
		}
		// The replacement search is looked for from the dispatch of this
		// manager request: the manager queues it in response.
		// Only commands newer than those listed in the step's baseline are
		// attributed to it.
		since, base, err := tx.StepDispatch(id, st.Seq)
		if err != nil {
			return err
		}
		if since == nil {
			since = &now
		}
		var b struct {
			MaxCommandID *int64 `json:"max_command_id"`
		}
		_ = json.Unmarshal(base, &b)
		return tx.CreateOutcome(store.RecoveryOutcome{IntentID: id, ManagerID: tg.ManagerID, Content: content, OldDownloadID: tg.DownloadID,
			OldClientID: sub.ClientID, StartedAt: now, Search: store.SearchOutcome{Since: since, AfterID: b.MaxCommandID}})
	}
	return nil
}

// Governing returns the workflow whose permission and pause apply.
func (p Proposal) Governing() policy.Workflow {
	if p.Permission != "" {
		return policy.Workflow(p.Permission)
	}
	return policy.Workflow(p.Workflow)
}
