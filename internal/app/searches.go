package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/notify"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

// Maximum live search candidates evaluated in a dispatch pass.
const maxSearchCandidates = 20

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
	in := action.SearchPlanInput{Item: *it, Origin: origin, ConfigRevision: s.cfg.Revision, Tested: action.Tested, Now: now, Limits: s.cfg.Limits(), Timing: s.cfg.Search}
	in.Scope = policy.Subject{ManagerID: managerID, ContentKey: key}
	if it.Origin == "pack" && it.OriginIntent != nil {
		originIntent, err := s.st.Intent(ctx, *it.OriginIntent)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		var origin struct {
			ClientID string `json:"client_id"`
			NativeID string `json:"native_id"`
		}
		if originIntent.ID != 0 && json.Unmarshal(originIntent.Subject, &origin) == nil && origin.ClientID != "" {
			in.Scope, err = s.packScope(ctx, managerID, origin.ClientID, origin.NativeID)
			if err != nil {
				return nil, err
			}
			in.Scope.ContentKey = key
		}
	}
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
	pw := policy.WorkflowMissingSearch
	if it.Origin == "pack" {
		pw = policy.WorkflowPartialPack
	}
	if p, ok := pauses[string(pw)]; ok {
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
	if pending, err := s.st.MissingContent(ctx, store.MissingFilter{State: store.MissingSearched, Limit: 1}); err != nil {
		return nil, err
	} else if len(pending) > 0 {
		in.Searching = fmt.Sprintf("%s is still following the search for %s; check its command queue if it remains running",
			pending[0].ManagerID, pending[0].ContentKey)
	}
	if last, err := s.st.LastMissingSearch(ctx); err != nil {
		return nil, err
	} else if in.Searching == "" && last != nil && now.Sub(*last) < s.cfg.Search.Spacing.Duration {
		in.Searching = fmt.Sprintf("the last search was sent %s ago; searches are at least %s apart (one at a time)",
			policy.HumanDuration(now.Sub(*last)), policy.HumanDuration(s.cfg.Search.Spacing.Duration))
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
// never-searched and least-recently-searched first. Cached blockers do
// not consume the limit on live evaluations. It runs only in
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
	case !s.cfg.MaybeEnabled(policy.WorkflowMissingSearch) && !s.cfg.MaybeEnabled(policy.WorkflowPartialPack):
		rep.Skipped = "neither missing_search nor partial_pack is enabled in any scope"
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
	if err := s.followSearches(ctx, s.now()); err != nil {
		return rep, err
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
		rep.Evaluated++
		rep, err = s.sendSearch(ctx, it, pv.Proposal, rep)
		if err != nil || rep.IntentID != 0 {
			return rep, err
		}
	}
	return rep, nil
}

// sendSearch executes one search and records its search state.
func (s *Service) sendSearch(ctx context.Context, it store.MissingRecord, p *action.Proposal, rep SearchReport) (SearchReport, error) {
	c := &action.Coordinator{Store: s.st, Executor: s.executor, Gate: gate{s}, Now: s.now, Actor: "automation"}
	res, err := c.Execute(ctx, *p)
	var ref *action.RefusedError
	if errors.Is(err, action.ErrJournal) {
		return rep, err
	}
	if errors.As(err, &ref) || (err != nil && res.IntentID == 0) {
		rep.Waiting = append(rep.Waiting, fmt.Sprintf("%s|%s refused before anything was sent: %v", it.ManagerID, it.ContentKey, err))
		return rep, nil
	}
	if err != nil {
		return rep, err
	}
	now := s.now()
	if res.State == store.IntentCancelled {
		next := now.Add(s.cfg.Polling.Interval.Duration)
		note := "the search was not sent: " + res.Detail
		rep.Waiting = append(rep.Waiting, it.ManagerID+"|"+it.ContentKey+": "+note)
		return rep, s.st.Write(ctx, func(tx *store.Tx) error {
			return tx.SetMissingSearch(it.ManagerID, it.ContentKey, store.MissingNotSearched, note, &res.IntentID, nil, &next, false)
		})
	}
	rep.Sent, rep.IntentID, rep.Outcome = it.ManagerID+"|"+it.ContentKey, res.IntentID, string(res.State)
	id := res.IntentID
	state, note := store.MissingSearched, "the manager accepted the search; whether it finds or grabs a release is observed in the manager's queue"
	next := now.Add(s.cfg.Search.Backoff(it.Searches + 1))
	sev := domain.SeverityNotice
	switch res.State {
	case store.IntentSucceeded:
	case store.IntentUncertain:
		state, note, sev = store.MissingUncertain, "the search request's outcome could not be established; held for review (not re-sent)", domain.SeverityError
	default:
		state, note, sev = store.MissingNotSearched, "the search was not accepted: "+res.Detail, domain.SeverityWarning
	}
	err = s.st.Write(ctx, func(tx *store.Tx) error {
		if res.State == store.IntentFailed {
			if err := tx.SetMissingSearch(it.ManagerID, it.ContentKey, state, note, &id, &now, &next, false); err != nil {
				return err
			}
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

// reportsRE matches the completion message of released Sonarr 4.0.20 and
// Radarr 6.4.4 searches ("… 0 reports downloaded."); other wording leaves
// the result unverified.
var reportsRE = regexp.MustCompile(`(\d+) reports? downloaded`)

// followSearches settles the outcome of recent searches from the managers'
// command lists: grabbed, nothing downloaded, failed, or not observed. It
// distinguishes the manager's report of downloads from acceptance; it does
// not evaluate source coverage.
func (s *Service) followSearches(ctx context.Context, now time.Time) error {
	items, err := s.st.MissingContent(ctx, store.MissingFilter{State: store.MissingSearched, Limit: 1000})
	if err != nil {
		return err
	}
	cmds := map[string][]arr.Command{}
	for _, it := range items {
		if it.State != store.MissingSearched || it.LastIntentID == nil || it.LastSearchAt == nil {
			continue
		}
		cid, err := s.searchCommandID(ctx, *it.LastIntentID)
		if err != nil {
			return err
		}
		if _, read := cmds[it.ManagerID]; !read {
			cmds[it.ManagerID] = nil
			if r, ok := s.executor.(*workflow.Recovery); ok {
				if cr, ok := r.Managers[it.ManagerID].(workflow.CommandReader); ok {
					if list, err := cr.Commands(ctx); err == nil {
						cmds[it.ManagerID] = list
					}
				}
			}
		}
		state, note := "", ""
		active := false
		for _, c := range cmds[it.ManagerID] {
			if cid == 0 || c.ID != cid {
				continue
			}
			switch c.Status {
			case "queued", "started":
				active = true
			case "completed":
				if m := reportsRE.FindStringSubmatch(c.Message); m != nil {
					if m[1] == "0" {
						state, note = store.MissingNoResult, "the search completed and the manager reported no release downloaded ("+c.Message+"); "+
							"that is not evidence that no release exists or that every source was searched"
						if cov, err := s.coverageNote(ctx, it.ManagerID, *it.LastSearchAt); err != nil {
							return err
						} else if cov != "" {
							note += "; " + cov
						}
					} else {
						state, note = store.MissingGrabbed, "the search completed and the manager reported "+m[1]+" release(s) downloaded"
					}
				} else {
					state, note = store.MissingEndUnknown, "the search completed, but the manager's message does not say whether anything was downloaded"
				}
			case "failed", "aborted", "cancelled", "orphaned":
				state, note = store.MissingSearchFail, "the manager's search command ended "+c.Status+": "+c.Message
			}
		}
		if state == "" && !active && now.Sub(*it.LastSearchAt) > s.cfg.Search.FollowWindow.Duration {
			state, note = store.MissingEndUnknown, "the search's end was not observed in the manager's command list within "+policy.HumanDuration(s.cfg.Search.FollowWindow.Duration)
		}
		if state == "" {
			continue
		}
		if err := s.st.Write(ctx, func(tx *store.Tx) error {
			return tx.SetMissingSearch(it.ManagerID, it.ContentKey, state, note, nil, nil, it.NextSearchAt, false)
		}); err != nil {
			return err
		}
	}
	return nil
}

// searchCommandID returns the command ID the manager returned for a search
// action, or 0 when it was not recorded.
func (s *Service) searchCommandID(ctx context.Context, intentID int64) (int64, error) {
	in, err := s.st.Intent(ctx, intentID)
	if err != nil {
		return 0, err
	}
	for _, st := range in.Steps {
		if st.Op != action.OpSearch {
			continue
		}
		var ev struct {
			Response struct {
				CommandID int64 `json:"command_id"`
			} `json:"response"`
		}
		_ = json.Unmarshal(st.Outcome, &ev)
		return ev.Response.CommandID, nil
	}
	return 0, nil
}

// coverageNote describes sources the manager had disabled when a search
// ran: contemporaneous evidence of incomplete coverage (possible impact),
// not proof that the search missed something. "" when none is known.
func (s *Service) coverageNote(ctx context.Context, managerID string, at time.Time) (string, error) {
	srcs, err := s.st.Sources(ctx, managerID)
	if err != nil || len(srcs) == 0 {
		return "", err
	}
	var down []string
	usable, unknown := 0, 0
	for _, src := range srcs {
		if !src.AutomaticSearch || src.Protocol != "torrent" {
			continue
		}
		usable++
		if !src.StatusKnown {
			unknown++
		}
		if src.Unavailable(at) {
			down = append(down, src.Name)
		}
	}
	switch {
	case usable == 0:
		return "coverage: no indexer was enabled for automatic torrent searches", nil
	case unknown > 0 && len(down) == 0:
		return "coverage could not be verified: the manager does not report indexer status (see its health checks)", nil
	case len(down) > 0:
		return fmt.Sprintf("coverage incomplete: %d of %d torrent indexers were disabled by the manager after failures (%s)", len(down), usable, strings.Join(down, ", ")), nil
	}
	return "", nil
}

// previewPack plans the clearance of a partial season pack.
func (s *Service) previewPack(ctx context.Context, f *domain.Finding, origin action.Origin, out *ActionPreview) (*ActionPreview, error) {
	now := s.now()
	in := action.PackPlanInput{Finding: *f, Origin: origin, ConfigRevision: s.cfg.Revision, Tested: action.Tested, Now: now}
	var err error
	in.Scope, err = s.packScope(ctx, f.Subject.ManagerID, f.Subject.ClientID, f.Subject.NativeID)
	if err != nil {
		return nil, err
	}
	if in.Policy, err = s.cfg.Resolve(in.Scope); err != nil {
		return nil, err
	}
	rows, err := s.st.QueueItems(ctx, store.QueueFilter{ManagerID: f.Subject.ManagerID, PresentOnly: true})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if strings.EqualFold(r.DownloadID, f.Subject.DownloadID) {
			in.Rows = append(in.Rows, r)
		}
	}
	recs, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range recs {
		if c.ID == f.Subject.ManagerID {
			in.ManagerKind, in.ManagerProbe = adapter.Kind(c.Kind), c.Probe
		}
		if f.Subject.ClientID != "" && c.ID == f.Subject.ClientID {
			in.ClientKind, in.ClientProbe = adapter.Kind(c.Kind), c.Probe
		}
	}
	pauses, err := s.st.WorkflowPauses(ctx)
	if err != nil {
		return nil, err
	}
	if p, ok := pauses[string(policy.WorkflowPartialPack)]; ok {
		in.Paused = &p
	}
	if in.Locks, err = s.st.HeldLocks(ctx); err != nil {
		return nil, err
	}
	if in.Cooldowns, err = s.st.ActiveCooldowns(ctx, now); err != nil {
		return nil, err
	}
	// An earlier clearance of the same download that succeeded: a tracked
	// association again means the clearance did not hold.
	done, err := s.st.Intents(ctx, store.IntentFilter{States: []store.IntentState{store.IntentSucceeded}, Limit: 500})
	if err != nil {
		return nil, err
	}
	for _, d := range done {
		var sub struct {
			DownloadID string `json:"download_id"`
		}
		if d.Workflow == string(policy.WorkflowPartialPack) && json.Unmarshal(d.Subject, &sub) == nil && strings.EqualFold(sub.DownloadID, f.Subject.DownloadID) {
			in.Handled = d.ID
		}
	}
	p := action.PlanPack(in)
	p.Prerequisites = append(p.Prerequisites, s.packContentBlockers(*p)...)
	if out.Journal.RestoreHold != nil {
		p.Prerequisites = append(p.Prerequisites, action.Check{Name: "state matches the action journal", Hard: true, Detail: out.Journal.RestoreHold.Reason})
	}
	if !out.Journal.Writable {
		p.Prerequisites = append(p.Prerequisites, action.Check{Name: "writable state store", Hard: true,
			Detail: "this process opened the state store read-only (another Housekeeparr process owns it)"})
	}
	out.Workflow, out.Proposal, out.Policy = p.Workflow, p, in.Policy
	return out, nil
}

// packScope preserves the originating transfer restrictions for both clearance
// and targeted follow-up.
func (s *Service) packScope(ctx context.Context, manager, client, native string) (policy.Subject, error) {
	var transfer *domain.Transfer
	if client != "" && native != "" {
		tr, err := s.st.Transfer(ctx, client, native)
		if err != nil {
			return policy.Subject{}, err
		}
		if tr != nil {
			transfer = &tr.Latest
		}
	}
	scope := policy.TransferSubject(client, native, transfer, nil)
	scope.ManagerID = manager
	return scope, nil
}

func (s *Service) packContentBlockers(p action.Proposal) []action.Check {
	var out []action.Check
	for _, key := range p.Planned.Content {
		scope := p.Scope
		scope.ContentKey = key
		eff, err := s.cfg.Resolve(scope)
		if err != nil {
			out = append(out, action.Check{Name: "policy for " + key, Hard: true, Detail: err.Error()})
			continue
		}
		if check := action.PolicyCheck(eff, policy.WorkflowPartialPack, p.Origin); !check.Satisfied {
			check.Detail = key + ": " + check.Detail
			out = append(out, check)
		}
	}
	return out
}
