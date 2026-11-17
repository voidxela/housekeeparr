package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Automatic dispatch bounds that are not operator policy: how many live
// evaluations (each a few reads) one cycle may spend, and the window and
// count for the repeated-refusal circuit breaker (implementation
// proposals).
const (
	maxLiveEvaluations = 5
	refusalWindow      = 24 * time.Hour
	refusalsToOpen     = 2
)

// DispatchReport summarises one automatic dispatch pass.
type DispatchReport struct {
	Skipped     string                       `json:"skipped,omitempty"`
	Evaluations []store.AutomationEvaluation `json:"evaluations,omitempty"`
	Breakers    map[string]string            `json:"breakers,omitempty"`
}

// Dispatched counts journaled actions.
func (r DispatchReport) Dispatched() int {
	n := 0
	for _, e := range r.Evaluations {
		if e.Result == store.EvalDispatched {
			n++
		}
	}
	return n
}

// DispatchAutomatic evaluates eligible findings for automatic action after
// an observation cycle, through the same planner, coordinator, and checks
// as manual actions. fresh names the connections read completely in that
// cycle; findings depending on any other connection wait. At most the
// configured number of actions are dispatched per pass, one at a time,
// oldest finding first while rotating across managers; a candidate
// refused before any write does not use a slot. Each evaluated finding's
// result is persisted so the decision stays visible.
func (s *Service) DispatchAutomatic(ctx context.Context, fresh map[string]bool) (DispatchReport, error) {
	var rep DispatchReport
	if s.executor == nil {
		rep.Skipped = "this process has no write executor"
		return rep, nil
	}
	if s.cfg.Mode != "automatic" {
		rep.Skipped = "mode is " + string(s.cfg.Mode) + "; automatic dispatch runs only in automatic mode"
		return rep, nil
	}
	if s.cfg.Automation.EmergencyStop {
		rep.Skipped = "emergency stop is set"
		return rep, nil
	}
	s.execMu.Lock()
	defer s.execMu.Unlock()
	if _, err := s.Reconcile(ctx); err != nil {
		return rep, fmt.Errorf("reconcile interrupted actions: %w", err)
	}
	if js, err := s.Journal(ctx); err != nil {
		return rep, err
	} else if js.RestoreHold != nil {
		rep.Skipped = "writes are held: " + js.RestoreHold.Reason
		return rep, nil
	}
	now := s.now()
	breakers, err := s.breakers(ctx, now)
	if err != nil {
		return rep, err
	}
	rep.Breakers = breakers
	cands, err := s.candidates(ctx, fresh)
	if err != nil {
		return rep, err
	}
	limit := s.cfg.Limits().ActionsPerCycle
	dispatched, live := 0, 0
	for _, f := range cands {
		if ctx.Err() != nil {
			break
		}
		ev := store.AutomationEvaluation{FindingID: f.ID, EvaluatedAt: now}
		pv, err := s.Preview(ctx, f.ID, action.OriginAutomatic)
		if err != nil {
			return rep, err
		}
		switch {
		case pv.Proposal == nil:
			ev.Result, ev.Detail = store.EvalIneligible, pv.NoAction
		case !pv.Proposal.Eligible():
			ev.Result, ev.Detail, ev.NextAt = s.classify(ctx, pv.Proposal, now)
		case heldBy(breakers, pv.Proposal) != "":
			ev.Result, ev.Detail = store.EvalHeld, heldBy(breakers, pv.Proposal)
		case dispatched >= limit || live >= maxLiveEvaluations:
			ev.Result = store.EvalWaiting
			ev.Detail = fmt.Sprintf("eligible; this pass already used its %d action slot(s) or live-evaluation bound; it is considered again next cycle", limit)
		default:
			live++
			c := &action.Coordinator{Store: s.st, Executor: s.executor, Gate: gate{s}, Now: s.now, Actor: "automation"}
			res, err := c.Execute(ctx, *pv.Proposal)
			var ref *action.RefusedError
			switch {
			case errors.As(err, &ref):
				ev.Result, ev.Detail = store.EvalRefused, "nothing was sent: "+ref.Error()
			case err != nil && res.IntentID == 0:
				ev.Result, ev.Detail = store.EvalRefused, "nothing was sent: "+err.Error()
			default:
				dispatched++
				ev.Result, ev.IntentID = store.EvalDispatched, res.IntentID
				ev.Detail = fmt.Sprintf("action #%d %s: %s", res.IntentID, res.State, res.Detail)
				if err != nil {
					ev.Detail += " (" + err.Error() + ")"
				}
			}
		}
		if err := s.st.Write(ctx, func(tx *store.Tx) error { return tx.SaveEvaluation(ev) }); err != nil {
			return rep, err
		}
		rep.Evaluations = append(rep.Evaluations, ev)
	}
	return rep, nil
}

// candidates returns active, unsnoozed findings with a workflow whose
// client (and correlated managers) were read completely this cycle, oldest
// first, interleaved across managers so one busy manager cannot starve
// the others.
func (s *Service) candidates(ctx context.Context, fresh map[string]bool) ([]domain.Finding, error) {
	fs, err := s.st.Findings(ctx, store.FindingFilter{States: []domain.FindingState{domain.FindingOpen, domain.FindingAcknowledged}})
	if err != nil {
		return nil, err
	}
	byManager := map[string][]domain.Finding{}
	for _, f := range fs {
		if _, ok := action.WorkflowFor(f.Type); !ok || f.Subject.ClientID == "" || !fresh[f.Subject.ClientID] {
			continue
		}
		// A finding already acted on keeps that decision visible: only a
		// failed or cancelled action (nothing took effect, or a reviewed
		// refusal) lets it be evaluated again.
		acted, err := s.st.Intents(ctx, store.IntentFilter{FindingID: f.ID})
		if err != nil {
			return nil, err
		}
		done := false
		for _, in := range acted {
			done = done || (in.State != store.IntentFailed && in.State != store.IntentCancelled)
		}
		if done {
			continue
		}
		rows, err := s.st.QueueItems(ctx, store.QueueFilter{ClientID: f.Subject.ClientID, NativeID: f.Subject.NativeID})
		if err != nil {
			return nil, err
		}
		mgr, ok := "", true
		for _, r := range rows {
			if r.Present {
				mgr = r.ManagerID
				ok = ok && fresh[r.ManagerID]
			}
		}
		if ok {
			byManager[mgr] = append(byManager[mgr], f)
		}
	}
	var keys []string
	for k, list := range byManager {
		sort.Slice(list, func(i, j int) bool { return list[i].FirstSeen.Before(list[j].FirstSeen) })
		keys = append(keys, k)
	}
	// Managers whose oldest finding is oldest go first.
	sort.Slice(keys, func(i, j int) bool { return byManager[keys[i]][0].FirstSeen.Before(byManager[keys[j]][0].FirstSeen) })
	var out []domain.Finding
	for round := 0; ; round++ {
		added := false
		for _, k := range keys {
			if round < len(byManager[k]) {
				out = append(out, byManager[k][round])
				added = true
			}
		}
		if !added {
			break
		}
	}
	// Findings a live check refused last time go behind the others, so a
	// few persistently refused ones (unmonitored content, say) cannot use
	// up every live evaluation and starve newer findings.
	var fresh2, refused []domain.Finding
	for _, f := range out {
		ev, err := s.st.Evaluation(ctx, f.ID)
		if err != nil {
			return nil, err
		}
		if ev != nil && ev.Result == store.EvalRefused {
			refused = append(refused, f)
		} else {
			fresh2 = append(fresh2, f)
		}
	}
	return append(fresh2, refused...), nil
}

// classify turns an ineligible proposal's blockers into a persisted
// result: pacing defers until its allowance returns, exhausted attempts
// need review, the retained ceiling needs cleanup, policy forbids.
func (s *Service) classify(ctx context.Context, p *action.Proposal, now time.Time) (string, string, *time.Time) {
	var names []string
	for _, b := range p.Blockers() {
		names = append(names, b.Name+": "+b.Detail)
	}
	detail := strings.Join(names, "; ")
	has := func(prefix string) bool {
		for _, b := range p.Blockers() {
			if strings.HasPrefix(b.Name, prefix) {
				return true
			}
		}
		return false
	}
	switch {
	case has("automation permitted by policy"), has("workflow not paused"):
		return store.EvalNotPermitted, detail, nil
	case has("recovery attempts for"):
		return store.EvalNeedsReview, detail, nil
	case has("retained-data ceiling"):
		return store.EvalAtCeiling, detail, nil
	case len(p.Blockers()) > 0 && allPacing(p.Blockers()):
		var next *time.Time
		for _, b := range p.Budgets {
			if b.Kind != store.BudgetPacing {
				continue
			}
			if u, err := s.st.BudgetUsage(ctx, b.Budget(), b.Units, now); err == nil && u.NextAt != nil && (next == nil || u.NextAt.After(*next)) {
				next = u.NextAt
			}
		}
		return store.EvalDeferred, detail, next
	}
	return store.EvalIneligible, detail, nil
}

func allPacing(cs []action.Check) bool {
	for _, c := range cs {
		if !strings.HasPrefix(c.Name, "acquisition pacing") {
			return false
		}
	}
	return true
}

// breakers returns the scopes ("client:<id>", "manager:<id>") automation
// must not act in, with the reason:
//   - an action involving the scope is uncertain: until it is reviewed;
//   - automatic actions there were definitely refused repeatedly within
//     the refusal window (for example a rejected credential), which would
//     otherwise repeat every cycle because refusals return their budget;
//   - many downloads on one client qualify as stalled together, which
//     suggests a shared cause (network, tracker, VPN) rather than bad
//     releases; failing them would only blocklist good releases.
func (s *Service) breakers(ctx context.Context, now time.Time) (map[string]string, error) {
	out := map[string]string{}
	open := func(scope, why string) {
		if _, ok := out[scope]; !ok {
			out[scope] = why
		}
	}
	unc, err := s.st.Intents(ctx, store.IntentFilter{States: []store.IntentState{store.IntentUncertain}})
	if err != nil {
		return nil, err
	}
	for _, in := range unc {
		for _, sc := range intentScopes(in) {
			open(sc, fmt.Sprintf("action #%d is uncertain and needs review (housekeeparr review %d)", in.ID, in.ID))
		}
	}
	recent, err := s.st.Intents(ctx, store.IntentFilter{States: []store.IntentState{store.IntentFailed}, Limit: 200})
	if err != nil {
		return nil, err
	}
	refusals := map[string]int{}
	for _, in := range recent {
		if in.Origin != string(action.OriginAutomatic) || now.Sub(in.UpdatedAt) > refusalWindow {
			continue
		}
		for _, sc := range intentScopes(in) {
			refusals[sc]++
			if refusals[sc] >= refusalsToOpen {
				open(sc, fmt.Sprintf("%d automatic actions were refused here in the last %s (latest #%d: %s)",
					refusals[sc], policy.HumanDuration(refusalWindow), in.ID, in.Explanation))
			}
		}
	}
	ts, err := s.st.Transfers(ctx, store.TransferFilter{PresentOnly: true})
	if err != nil {
		return nil, err
	}
	// A torrent whose grab an earlier recovery already failed (for example
	// a private one left running) is no longer a recovery candidate: it is
	// left out of both counts, so it neither triggers nor dilutes the hold.
	recovered, err := s.st.RecoveredDownloads(ctx)
	if err != nil {
		return nil, err
	}
	active, stalled := map[string]int{}, map[string]int{}
	for _, t := range ts {
		// Metadata acquisition counts too: magnets stalling together on
		// one client point to DHT, network or VPN trouble.
		if t.StateClass != domain.StateDownloading && t.StateClass != domain.StateMetadata {
			continue
		}
		if _, done := recovered[strings.ToLower(t.NativeID)]; done {
			continue
		}
		active[t.ClientID]++
		if t.Assessment != nil && t.Assessment.Status == policy.StatusQualified {
			stalled[t.ClientID]++
		}
	}
	l := s.cfg.Limits()
	for c, n := range stalled {
		if n >= l.CorrelatedStalls && 100*n >= l.CorrelatedPercent*active[c] {
			open("client:"+c, fmt.Sprintf("%d of %d active downloads on %s are stalled together (threshold %d and %d%%, from %s); that points to a shared "+
				"cause (network, tracker, VPN, or the client itself) rather than bad releases, so automatic recovery is held there until fewer are stalled "+
				"(manual actions remain possible)", n, active[c], c, l.CorrelatedStalls, l.CorrelatedPercent, l.From("correlated_stalls")))
		}
	}
	return out, nil
}

// intentScopes returns the client and manager scopes an intent touched.
func intentScopes(in store.Intent) []string {
	var out []string
	var sub struct {
		ClientID string `json:"client_id"`
	}
	if json.Unmarshal(in.Subject, &sub) == nil && sub.ClientID != "" {
		out = append(out, "client:"+sub.ClientID)
	}
	for _, st := range in.Steps {
		var tg struct {
			ManagerID string `json:"manager_id"`
		}
		if json.Unmarshal(st.Target, &tg) == nil && tg.ManagerID != "" {
			out = append(out, "manager:"+tg.ManagerID)
		}
	}
	return out
}

// heldBy returns the breaker reason holding any scope of p, or "".
func heldBy(breakers map[string]string, p *action.Proposal) string {
	for _, sc := range []string{"client:" + p.Planned.ClientID, "manager:" + p.Planned.ManagerID} {
		if why, ok := breakers[sc]; ok {
			return sc + " is held: " + why
		}
	}
	return ""
}

// AutomationEvaluation returns a finding's latest automatic evaluation.
func (s *Service) AutomationEvaluation(ctx context.Context, findingID int64) (*store.AutomationEvaluation, error) {
	return s.st.Evaluation(ctx, findingID)
}
