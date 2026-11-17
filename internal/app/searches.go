package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/notify"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Proactive-search pacing (implementation proposals, not validated
// defaults): one search at a time, at least SearchSpacing apart across the
// installation, at most maxSearchCandidates evaluated per pass. The
// acquisition budgets ([automation.limits]) bound them further.
const (
	SearchSpacing       = 15 * time.Minute
	maxSearchCandidates = 20
)

// SearchPreview is the proposal for searching one missing item.
type SearchPreview struct {
	Item     store.MissingRecord `json:"item"`
	Journal  JournalStatus       `json:"journal"`
	Proposal *action.Proposal    `json:"proposal"`
}

// PreviewSearch plans a search for one missing item from local state.
func (s *Service) PreviewSearch(ctx context.Context, managerID, key string, origin action.Origin) (*SearchPreview, error) {
	it, err := s.st.MissingRecord(ctx, managerID, key)
	if err != nil {
		return nil, err
	}
	if it == nil {
		return nil, ErrNotFound
	}
	out := &SearchPreview{Item: *it}
	if out.Journal, err = s.Journal(ctx); err != nil {
		return nil, err
	}
	now := s.now()
	in := action.SearchPlanInput{Item: *it, Origin: origin, ConfigRevision: s.cfg.Revision, Tested: action.Tested, Now: now, Limits: s.cfg.Limits()}
	in.Scope = policy.Subject{ManagerID: managerID, ContentKey: key}
	if in.Policy, err = s.cfg.Resolve(in.Scope); err != nil {
		return nil, err
	}
	recs, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range recs {
		if c.ID == managerID {
			in.ManagerKind, in.ManagerProbe = adapter.Kind(c.Kind), c.Probe
		}
	}
	if in.Queue, err = s.st.QueueItems(ctx, store.QueueFilter{ManagerID: managerID, PresentOnly: true}); err != nil {
		return nil, err
	}
	pauses, err := s.st.WorkflowPauses(ctx)
	if err != nil {
		return nil, err
	}
	if p, ok := pauses[string(policy.WorkflowMissingSearch)]; ok {
		in.Paused = &p
	}
	if in.Retained, err = s.st.RetainedUsage(ctx); err != nil {
		return nil, err
	}
	if in.Locks, err = s.st.HeldLocks(ctx); err != nil {
		return nil, err
	}
	if in.Cooldowns, err = s.st.ActiveCooldowns(ctx, now); err != nil {
		return nil, err
	}
	if last, err := s.st.LastMissingSearch(ctx); err != nil {
		return nil, err
	} else if last != nil && now.Sub(*last) < SearchSpacing {
		in.Searching = fmt.Sprintf("the last search was sent %s ago; searches are at least %s apart (one at a time)",
			policy.HumanDuration(now.Sub(*last)), policy.HumanDuration(SearchSpacing))
	}
	p := action.PlanSearch(in)
	usage := map[string]store.BudgetUsage{}
	for _, b := range p.Budgets {
		u, err := s.st.BudgetUsage(ctx, b.Budget(), b.Units, now)
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
	out.Proposal = p
	return out, nil
}

// SearchReport is one proactive-search pass.
type SearchReport struct {
	Skipped   string   `json:"skipped,omitempty"`
	Evaluated int      `json:"evaluated"`
	Sent      string   `json:"sent,omitempty"` // manager|content of the search sent
	IntentID  int64    `json:"intent_id,omitempty"`
	Outcome   string   `json:"outcome,omitempty"`
	Waiting   []string `json:"waiting,omitempty"` // first blockers of the leading candidates
}

// DispatchSearches sends at most one proactive search after a cycle: the
// first eligible missing item of a manager read completely this cycle,
// never-searched and least-recently-searched first. It runs only in
// automatic mode with missing_search enabled for the item's scope.
func (s *Service) DispatchSearches(ctx context.Context, fresh map[string]bool) (SearchReport, error) {
	var rep SearchReport
	switch {
	case s.executor == nil:
		rep.Skipped = "this process has no write executor"
	case s.cfg.Mode != "automatic":
		rep.Skipped = "mode is " + string(s.cfg.Mode)
	case s.cfg.Automation.EmergencyStop:
		rep.Skipped = "emergency stop is set"
	case !s.cfg.MaybeEnabled(policy.WorkflowMissingSearch):
		rep.Skipped = "missing_search is not enabled in any scope"
	}
	if rep.Skipped != "" {
		return rep, nil
	}
	s.execMu.Lock()
	defer s.execMu.Unlock()
	if js, err := s.Journal(ctx); err != nil {
		return rep, err
	} else if js.RestoreHold != nil {
		rep.Skipped = "writes are held: " + js.RestoreHold.Reason
		return rep, nil
	}
	items, err := s.st.MissingContent(ctx, store.MissingFilter{PresentOnly: true, Limit: 500})
	if err != nil {
		return rep, err
	}
	for _, it := range items {
		if rep.Evaluated >= maxSearchCandidates || ctx.Err() != nil {
			break
		}
		if !fresh[it.ManagerID] {
			continue
		}
		rep.Evaluated++
		pv, err := s.PreviewSearch(ctx, it.ManagerID, it.ContentKey, action.OriginAutomatic)
		if err != nil {
			return rep, err
		}
		if b := pv.Proposal.Blockers(); len(b) > 0 {
			if len(rep.Waiting) < 5 {
				rep.Waiting = append(rep.Waiting, fmt.Sprintf("%s|%s: %s — %s", it.ManagerID, it.ContentKey, b[0].Name, b[0].Detail))
			}
			// Spacing applies to every item alike: stop here.
			if b[0].Name == "no search in progress" {
				break
			}
			continue
		}
		return s.sendSearch(ctx, it, pv.Proposal, rep)
	}
	return rep, nil
}

// sendSearch executes one search and records its search state.
func (s *Service) sendSearch(ctx context.Context, it store.MissingRecord, p *action.Proposal, rep SearchReport) (SearchReport, error) {
	c := &action.Coordinator{Store: s.st, Executor: s.executor, Gate: gate{s}, Now: s.now, Actor: "automation"}
	res, err := c.Execute(ctx, *p)
	var ref *action.RefusedError
	if errors.As(err, &ref) || (err != nil && res.IntentID == 0) {
		rep.Waiting = append(rep.Waiting, fmt.Sprintf("%s|%s refused before anything was sent: %v", it.ManagerID, it.ContentKey, err))
		return rep, nil
	}
	now := s.now()
	rep.Sent, rep.IntentID, rep.Outcome = it.ManagerID+"|"+it.ContentKey, res.IntentID, string(res.State)
	id := res.IntentID
	state, note := store.MissingSearched, "the manager accepted the search; whether it finds or grabs a release is observed in the manager's queue"
	next := now.Add(action.SearchBackoff(it.Searches + 1))
	sev := domain.SeverityNotice
	switch res.State {
	case store.IntentSucceeded:
	case store.IntentUncertain:
		state, note, sev = store.MissingUncertain, "the search request's outcome could not be established; held for review (not re-sent)", domain.SeverityError
	default:
		state, note, sev = store.MissingNotSearched, "the search was not accepted: "+res.Detail, domain.SeverityWarning
	}
	err = s.st.Write(ctx, func(tx *store.Tx) error {
		count := res.State == store.IntentSucceeded || res.State == store.IntentUncertain
		if err := tx.SetMissingSearch(it.ManagerID, it.ContentKey, state, note, &id, &now, &next, count); err != nil {
			return err
		}
		return notify.EnqueueEvent(tx, s.cfg.Notify, notify.Event{DedupKey: fmt.Sprintf("search|%d", id), Kind: notify.KindAction, Severity: sev,
			Title: "Search for missing " + it.Title + ": " + string(res.State), Body: note}, now)
	})
	return rep, err
}

// Missing lists recorded missing content and its search state.
func (s *Service) Missing(ctx context.Context, presentOnly bool) ([]store.MissingRecord, error) {
	return s.st.MissingContent(ctx, store.MissingFilter{PresentOnly: presentOnly, Limit: 1000})
}
