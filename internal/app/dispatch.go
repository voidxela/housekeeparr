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
	"github.com/voidxela/housekeeparr/internal/notify"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Limit the live reads spent evaluating recovery proposals in one cycle.
const maxLiveEvaluations = 5

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
	breakers, corr, err := s.breakers(ctx, now)
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
		var outcome store.IntentState
		pv, err := s.Preview(ctx, f.ID, action.OriginAutomatic)
		if err != nil {
			return rep, err
		}
		held, probe, exempt := "", "", false
		if pv.Proposal != nil {
			held = heldBy(breakers, pv.Proposal)
			// A correlated hold refuted by progress elsewhere on the client
			// lets one probe through; any other breaker still holds.
			c := "client:" + pv.Proposal.Planned.ClientID
			switch h, ok := corr[c]; {
			case held == "" || !ok || breakers["manager:"+pv.Proposal.Planned.ManagerID] != "":
			case h.reached[pv.Proposal.Planned.NativeID]:
				held, exempt = "", true
			case h.probe:
				held, probe = "", c
			}
		}
		switch {
		case pv.Proposal == nil:
			ev.Result, ev.Detail = store.EvalIneligible, pv.NoAction
		case !pv.Proposal.Eligible():
			ev.Result, ev.Detail, ev.NextAt = s.classify(ctx, pv.Proposal, now)
		case held != "":
			ev.Result, ev.Detail = store.EvalHeld, held
			if h, ok := corr["client:"+pv.Proposal.Planned.ClientID]; ok {
				ev.NextAt = h.next
			}
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
				ev.Result, ev.IntentID, outcome = store.EvalDispatched, res.IntentID, res.State
				ev.Detail = fmt.Sprintf("action #%d %s: %s", res.IntentID, res.State, res.Detail)
				if exempt {
					ev.Detail = "the correlated-stall hold does not apply: this download's connected peers lack its missing pieces, so its swarm " +
						"was reached and its stall is its own: " + ev.Detail
				}
				if probe != "" {
					// The probe is used: the rest of the client stays held.
					ev.Detail = "correlated-stall probe (" + corr[probe].reason + "): " + ev.Detail
					h := corr[probe]
					h.probe = false
					next := now.Add(s.cfg.Limits().CorrelatedProbeInterval)
					h.next = &next
					corr[probe] = h
				}
				if err != nil {
					ev.Detail += " (" + err.Error() + ")"
				}
			}
		}
		notes := []notify.Event{}
		if note, ok := automationNotice(f, ev, outcome, now); ok {
			notes = append(notes, note)
		}
		if ev.Result == store.EvalHeld && pv.Proposal != nil {
			if h, ok := corr["client:"+pv.Proposal.Planned.ClientID]; ok && !h.probing {
				notes = append(notes, correlatedHoldNotice(pv.Proposal.Planned.ClientID, h.reason, now))
			}
		}
		if err := s.st.Write(ctx, func(tx *store.Tx) error {
			if err := tx.SaveEvaluation(ev); err != nil {
				return err
			}
			for _, n := range notes {
				if err := notify.EnqueueEvent(tx, s.cfg.Notify, n, now); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
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

// actionBreakers returns the scopes ("client:<id>", "manager:<id>") automation
// must not act in, with the reason:
//   - an action involving the scope is uncertain: until it is reviewed;
//   - automatic actions there were definitely refused repeatedly within
//     the refusal window (for example a rejected credential), which would
//     otherwise repeat every cycle because refusals return their budget;
func (s *Service) actionBreakers(ctx context.Context, now time.Time) (map[string]string, error) {
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
	timing := s.cfg.ActionTiming
	recent, err := s.st.Intents(ctx, store.IntentFilter{States: []store.IntentState{store.IntentFailed},
		Origin: string(action.OriginAutomatic), UpdatedSince: now.Add(-timing.RefusalWindow.Duration)})
	if err != nil {
		return nil, err
	}
	refusals := map[string]int{}
	for _, in := range recent {
		for _, step := range in.Steps {
			if step.State != store.StepFailed {
				continue
			}
			var target struct {
				ManagerID string `json:"manager_id"`
				ClientID  string `json:"client_id"`
			}
			if json.Unmarshal(step.Target, &target) != nil {
				continue
			}
			sc := ""
			switch {
			case target.ManagerID != "":
				sc = "manager:" + target.ManagerID
			case target.ClientID != "":
				sc = "client:" + target.ClientID
			default:
				continue
			}
			refusals[sc]++
			if refusals[sc] == timing.RefusalsToOpen {
				open(sc, fmt.Sprintf("%d automatic requests were refused here in the last %s (including action #%d: %s)",
					refusals[sc], policy.HumanDuration(timing.RefusalWindow.Duration), in.ID, in.Explanation))
			}
		}
	}
	return out, nil
}

// correlatedHold is a correlated-stall hold on one client. probing means
// another download there made verified progress within its own no-progress
// window, refuting a client-wide cause (network, VPN, the client itself):
// one recovery may then pass per probe interval (probe), and next is when
// the following one may. A tracker-wide outage is not refuted by progress
// on other trackers, which is why probes are spaced rather than unlimited.
type correlatedHold struct {
	reason  string
	probing bool
	probe   bool
	next    *time.Time
	// reached holds the native IDs of stalls whose connected peers lack
	// their pieces; the hold does not apply to them.
	reached map[string]bool
}

func (s *Service) breakers(ctx context.Context, now time.Time) (map[string]string, map[string]correlatedHold, error) {
	out, err := s.actionBreakers(ctx, now)
	if err != nil {
		return nil, nil, err
	}
	ts, err := s.st.Transfers(ctx, store.TransferFilter{PresentOnly: true})
	if err != nil {
		return nil, nil, err
	}
	// A transfer whose grab an earlier recovery already failed (for example
	// a private one left running) is no longer a recovery candidate: it is
	// left out of both counts, so it neither triggers nor dilutes the hold.
	// Only that physical transfer (client plus native ID) is exempt, and
	// only while no manager associates it again: a new grab of the same
	// release makes it a candidate like any other.
	recovered, err := s.st.RecoveredDownloads(ctx)
	if err != nil {
		return nil, nil, err
	}
	qs, err := s.st.QueueItems(ctx, store.QueueFilter{PresentOnly: true})
	if err != nil {
		return nil, nil, err
	}
	associated := map[string]bool{}
	for _, q := range qs {
		if q.MatchClientID != "" {
			associated[store.HandledKey(q.MatchClientID, q.MatchNativeID)] = true
		}
	}
	active, stalled, progressing, contact := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	reached := map[string]map[string]bool{}
	for _, t := range ts {
		// Metadata acquisition counts too: magnets stalling together on
		// one client point to DHT, network or VPN trouble.
		if t.StateClass != domain.StateDownloading && t.StateClass != domain.StateMetadata {
			continue
		}
		if k := store.HandledKey(t.ClientID, t.NativeID); recovered[k] != 0 && !associated[k] {
			continue
		}
		active[t.ClientID]++
		peers, _ := t.Latest.ConnectedPeers.Get()
		if peers > 0 {
			contact[t.ClientID]++
		}
		a := t.Assessment
		switch {
		case a == nil:
		case a.Status == policy.StatusQualified && a.Diagnosis == policy.DiagnosisNoProgressUnavailable && peers > 0:
			// Its connected peers lack its pieces: the network, client
			// and tracker reached its swarm, so its stall is its own and
			// is no evidence of a shared cause.
			if reached[t.ClientID] == nil {
				reached[t.ClientID] = map[string]bool{}
			}
			reached[t.ClientID][t.NativeID] = true
		case a.Status == policy.StatusQualified:
			stalled[t.ClientID]++
		case a.LastProgressAt != nil && a.Window > 0 && now.Sub(*a.LastProgressAt) <= a.Window:
			progressing[t.ClientID]++
		}
	}
	l := s.cfg.Limits()
	corr := map[string]correlatedHold{}
	var lastProbe map[string]time.Time
	for c, n := range stalled {
		sc := "client:" + c
		if out[sc] != "" || n < l.CorrelatedStalls || 100*n < l.CorrelatedPercent*active[c] {
			continue
		}
		why := fmt.Sprintf("%d of %d active downloads on %s are stalled together (threshold %d and %d%%, from %s); that points to a shared "+
			"cause (network, tracker, VPN, or the client itself) rather than bad releases, so automatic recovery is held there until fewer are stalled "+
			"(manual actions remain possible)", n, active[c], c, l.CorrelatedStalls, l.CorrelatedPercent, l.From("correlated_stalls"))
		h := correlatedHold{reason: why, reached: reached[c]}
		if p, k := progressing[c], contact[c]; (p > 0 || k > 0) && l.CorrelatedProbeInterval > 0 {
			if lastProbe == nil {
				if lastProbe, err = s.lastAutomaticByClient(ctx, now.Add(-l.CorrelatedProbeInterval)); err != nil {
					return nil, nil, err
				}
			}
			h.probing = true
			if p > 0 {
				h.reason = fmt.Sprintf("%d of %d active downloads on %s are stalled together, but %d other download(s) there made verified progress within "+
					"their window, so the client is downloading", n, active[c], c, p)
			} else {
				h.reason = fmt.Sprintf("%d of %d active downloads on %s are stalled together, but %d active download(s) there are connected to peers, "+
					"so the client and its network work", n, active[c], c, k)
			}
			if at, ok := lastProbe[c]; ok {
				next := at.Add(l.CorrelatedProbeInterval)
				h.next = &next
				why = h.reason + fmt.Sprintf("; one recovery there passes as a probe every %s (%s), next after %s",
					policy.HumanDuration(l.CorrelatedProbeInterval), l.From("correlated_probe_interval"), next.UTC().Format(time.RFC3339))
			} else {
				h.probe = true
				why = h.reason
			}
		}
		out[sc] = why
		corr[sc] = h
	}
	return out, corr, nil
}

// lastAutomaticByClient returns, per client, the newest automatic action
// created since the given time.
func (s *Service) lastAutomaticByClient(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	ins, err := s.st.Intents(ctx, store.IntentFilter{Origin: string(action.OriginAutomatic), UpdatedSince: since})
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, in := range ins {
		if in.CreatedAt.Before(since) {
			continue
		}
		for _, sc := range intentScopes(in) {
			if c, ok := strings.CutPrefix(sc, "client:"); ok && in.CreatedAt.After(out[c]) {
				out[c] = in.CreatedAt
			}
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

// correlatedHoldNotice tells the user, once a day per client, that
// recovery there is held with no evidence that the client can download:
// only they can check the network, VPN, tracker or client.
func correlatedHoldNotice(clientID, reason string, now time.Time) notify.Event {
	return notify.Event{DedupKey: "correlated_hold|" + clientID + "|" + now.UTC().Format("2006-01-02"), Kind: notify.KindAttention,
		Severity: domain.SeverityWarning, Title: "Automatic recovery held on " + clientID + ": downloads stalled together",
		Body: reason + "\nNo download there made verified progress within its window. Check the client's network, VPN and trackers; " +
			"recovery resumes by itself once fewer downloads are stalled or one makes progress."}
}

// automationNotice is the notification, if any, for an automatic
// evaluation: an action taken (its outcome), or automation stopped for a
// reason only the user can resolve. Pacing, waiting, and holds that clear
// by themselves are not notified.
func automationNotice(f domain.Finding, ev store.AutomationEvaluation, outcome store.IntentState, now time.Time) (notify.Event, bool) {
	switch ev.Result {
	case store.EvalDispatched:
		sev, verb := domain.SeverityNotice, "Grab failed and replacement requested"
		switch outcome {
		case store.IntentSucceeded:
		case store.IntentUncertain:
			sev, verb = domain.SeverityError, "Action needs review"
		default:
			sev, verb = domain.SeverityWarning, "Action did not complete"
		}
		return notify.Event{DedupKey: fmt.Sprintf("action|%d", ev.IntentID), Kind: notify.KindAction, Severity: sev, FindingID: f.ID,
			Title: verb + ": " + f.Title, Body: ev.Detail}, true
	case store.EvalNeedsReview:
		return notify.Event{DedupKey: fmt.Sprintf("needs_review|%d", f.ID), Kind: notify.KindAttention, Severity: domain.SeverityWarning, FindingID: f.ID,
			Title: "Replacement attempts exhausted: " + f.Title, Body: ev.Detail + "\nReview the content, then reset its attempts (housekeeparr attempts reset) to allow recovery again."}, true
	case store.EvalAtCeiling:
		// One per day: every candidate is at the same ceiling.
		return notify.Event{DedupKey: "at_ceiling|" + now.UTC().Format("2006-01-02"), Kind: notify.KindAttention, Severity: domain.SeverityWarning,
			Title: "Recovery stopped at the retained-data ceiling", Body: ev.Detail + "\nClean up retained transfers in the client (housekeeparr retained) or raise the ceiling."}, true
	}
	return notify.Event{}, false
}
