package action

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Proactive missing-content searches (SPEC §9.1). A search is one manager
// command for one monitored, available, missing episode or movie. It goes
// through the same coordinator as recovery: journaled before it is sent,
// locked per content, paced by the acquisition budgets, gated by the
// retained-data ceiling, and revalidated live before the request.

// OpSearch is the manager search operation.
const OpSearch = "manager.search"

// MissingFreshness bounds how old a missing-list read may be for planning
// (twice the engine's hourly read; an implementation proposal).
const MissingFreshness = 2 * time.Hour

// SearchBackoff is how long after its n-th search (n ≥ 1) an item that is
// still missing waits before it is searched again: one day, doubling, at
// most 30 days. An implementation proposal, not a validated default.
func SearchBackoff(n int) time.Duration {
	d := 24 * time.Hour
	for i := 1; i < n && d < 30*24*time.Hour; i++ {
		d *= 2
	}
	if d > 30*24*time.Hour {
		d = 30 * 24 * time.Hour
	}
	return d
}

// SearchPlanInput is everything PlanSearch needs, read from local state.
type SearchPlanInput struct {
	Item         store.MissingRecord
	Queue        []store.QueueRecord // present rows of the item's manager
	Policy       policy.Effective
	Scope        policy.Subject
	Origin       Origin
	ManagerKind  adapter.Kind
	ManagerProbe *adapter.Probe
	Locks        map[string]int64
	Cooldowns    map[string]store.CooldownRecord
	Limits       policy.Limits
	Paused       *store.WorkflowPause
	Retained     store.RetainedUsage
	// Searching is another search still in progress anywhere ("" none):
	// one acquisition search is in flight at a time.
	Searching      string
	Tested         []TestedWorkflow
	ConfigRevision string
	Now            time.Time
	PreviewTTL     time.Duration
}

// SearchTarget is the manager.search operation's target.
type SearchTarget struct {
	ManagerID string  `json:"manager_id"`
	Kind      string  `json:"content_kind"`
	IDs       []int64 `json:"ids"`
	Key       string  `json:"content_key"`
}

// PlanSearch builds the proposal for searching one missing item.
func PlanSearch(in SearchPlanInput) *Proposal {
	it := in.Item
	wf := policy.WorkflowMissingSearch
	limits := in.Limits
	if limits.Acquisitions == 0 {
		limits = policy.DefaultLimits()
	}
	ttl := in.PreviewTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	kind, idText, _ := strings.Cut(it.ContentKey, ":")
	id, _ := strconv.ParseInt(idText, 10, 64)
	p := &Proposal{
		Workflow: string(wf), Origin: in.Origin, Subject: raw(map[string]string{"manager_id": it.ManagerID, "content": it.ContentKey}),
		Locks: []string{ContentLock(it.ManagerID, it.ContentKey)}, PolicyRevision: in.Policy.Revision, ConfigRevision: in.ConfigRevision,
		Scope: in.Scope, CreatedAt: in.Now, ExpiresAt: in.Now.Add(ttl),
		LiveChecks: []string{"the content still exists and is monitored in its manager", "no queue item for the content (queued, grabbed, or importing)",
			"the manager version is unchanged"},
	}
	add := func(name string, ok bool, detail string) {
		p.Prerequisites = append(p.Prerequisites, Check{Name: name, Satisfied: ok, Hard: true, Detail: detail})
	}
	mv := probeVersion(in.ManagerProbe)
	tested := false
	for _, tw := range in.Tested {
		tested = tested || tw.Workflow == wf && tw.Manager == in.ManagerKind && tw.ManagerVersion == mv
	}
	combo := nonEmpty(string(in.ManagerKind), "an unknown manager") + " " + nonEmpty(mv, "(version unknown)")
	if tested {
		add("tested write workflow", true, "missing-content searches passed disposable-instance tests for "+combo)
	} else {
		add("tested write workflow", false, "no missing-content search workflow has been tested for "+combo+"; this build sends no searches there")
	}
	add("content identity known", id > 0 && (kind == "episode" || kind == "movie"), "content "+it.ContentKey)
	fresh := it.Present && in.Now.Sub(it.LastSeen) <= MissingFreshness
	add("still missing (fresh)", fresh, fmt.Sprintf("listed as missing by %s %s ago", it.ManagerID, policy.HumanDuration(in.Now.Sub(it.LastSeen))))
	avail := it.Available != nil && *it.Available
	add("released and available", avail, map[bool]string{true: "the manager reports it available (aired or released per its availability settings)",
		false: "the manager does not report it available yet, or availability is unknown; unknown is not available"}[avail])
	var queued []string
	for _, q := range in.Queue {
		if k, ok := q.Latest.ContentKey(); ok && q.Present && q.ManagerID == it.ManagerID && k == it.ContentKey {
			queued = append(queued, fmt.Sprintf("queue item %d (%q)", q.QueueID, q.Latest.Title))
		}
	}
	add("not queued", len(queued) == 0, nonEmpty(strings.Join(queued, "; "), "no queue item is downloading or importing it"))
	due := it.NextSearchAt == nil || !in.Now.Before(*it.NextSearchAt)
	d := fmt.Sprintf("searched %d time(s) by Housekeeparr", it.Searches)
	if !due {
		d += fmt.Sprintf("; next search not before %s (backoff after a search that found nothing grabbed)", it.NextSearchAt.UTC().Format(time.RFC3339))
	}
	add("not backing off", due, d)
	add("no search in progress", in.Searching == "", nonEmpty(in.Searching, "no other Housekeeparr search is running (one at a time)"))
	var held []string
	for _, l := range p.Locks {
		if h := in.Locks[l]; h != 0 {
			held = append(held, fmt.Sprintf("action #%d holds %s", h, l))
		}
		if c, ok := in.Cooldowns[l]; ok && in.Now.Before(c.Until) {
			held = append(held, fmt.Sprintf("%s in cooldown until %s (%s)", l, c.Until.UTC().Format(time.RFC3339), c.Reason))
		}
	}
	add("no conflicting action", len(held) == 0, nonEmpty(strings.Join(held, "; "), "nothing holds "+p.Locks[0]))
	p.Prerequisites = append(p.Prerequisites, PolicyCheck(in.Policy, wf, in.Origin), PauseCheck(in.Paused, wf), capacityCheck(in.Retained, limits))
	p.Budgets = acquisitionClaims(limits, it.ManagerID, nil)
	name := map[string]string{"episode": "EpisodeSearch", "movie": "MoviesSearch"}[kind]
	p.Operations = []Operation{{
		Op:          OpSearch,
		Target:      raw(SearchTarget{ManagerID: it.ManagerID, Kind: kind, IDs: []int64{id}, Key: it.ContentKey}),
		Request:     raw(map[string]any{"name": name, "ids": []int64{id}}),
		Expect:      raw(map[string]any{"command_listed": true}),
		Description: fmt.Sprintf("Ask %s to search for %s (%s)", it.ManagerID, it.Title, name),
	}}
	p.Effects = []string{fmt.Sprintf("%s searches its enabled indexers for %s alone (never a season or a broad search) and may grab a release under its own profiles", it.ManagerID, it.Title)}
	p.SideEffects = []string{
		"one manager search can query every enabled indexer and grab a release; Housekeeparr counts it as one acquisition but cannot bound the manager's own requests",
		fmt.Sprintf("if it is still missing afterwards, it is not searched again for %s (then longer, up to 30 days); a completed search does not mean a release exists or was grabbed",
			policy.HumanDuration(SearchBackoff(it.Searches+1))),
	}
	p.Planned = Planned{ManagerID: it.ManagerID, ManagerVersion: mv, Content: []string{it.ContentKey}}
	p.Evidence = raw(map[string]any{"missing": it})
	p.Fingerprint = digest(fmt.Sprintf("search|%s|%s|%d|%s", it.ManagerID, it.ContentKey, it.Searches, mv))[:16]
	return p
}
