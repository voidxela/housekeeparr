package action

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

func planInput() PlanInput {
	now := t0
	tr := &store.TransferRecord{ClientID: "qbit", NativeID: "aa", Name: "Show.S01E01", StateClass: domain.StateDownloading,
		LastSeen: now.Add(-time.Minute), Present: true,
		Latest:     domain.Transfer{NativeState: "stalledDL", Private: domain.KnownValue(false), VerifiedBytes: domain.KnownValue[int64](10)},
		Assessment: &policy.Assessment{Status: policy.StatusQualified, Diagnosis: policy.DiagnosisNoProgress, Headline: "no verified progress for 2h"}}
	q := store.QueueRecord{ManagerID: "sonarr", ItemKey: "aa|episode:1", QueueID: 7, DownloadID: "aa", Present: true, MatchStatus: store.MatchMatched,
		Latest: domain.QueueItem{ManagerID: "sonarr", Title: "Show.S01E01", DownloadID: "aa", ContentKind: "episode", SeriesID: 3, EpisodeID: 1}}
	eff := policy.Effective{Mode: "automatic", Revision: "rev", Automation: []policy.WorkflowPermission{{Workflow: policy.WorkflowNoProgressRecovery, Enabled: true, From: "test"}}}
	return PlanInput{
		Finding:  domain.Finding{ID: 3, Type: domain.FindingTransferStalled, State: domain.FindingOpen},
		Transfer: tr, Queue: []store.QueueRecord{q}, Policy: eff, Origin: OriginAutomatic,
		ClientKind: adapter.KindQBittorrent, ClientProbe: &adapter.Probe{Version: "v5.3.0"},
		ManagerKind: adapter.KindSonarr, ManagerProbe: &adapter.Probe{Version: "4.0.20.3014"},
		PollInterval: 10 * time.Minute, Now: now,
	}
}

func blockerNames(p *Proposal) []string {
	var out []string
	for _, b := range p.Blockers() {
		out = append(out, b.Name)
	}
	return out
}

func TestPlanBlockedOnlyByMissingTestedWorkflow(t *testing.T) {
	p, np := Plan(planInput())
	if np != nil {
		t.Fatal(np.Reason)
	}
	if b := blockerNames(p); len(b) != 1 || b[0] != "tested write workflow" {
		t.Fatalf("blockers %v", b)
	}
	if !strings.Contains(p.Blockers()[0].Detail, "sonarr 4.0.20.3014 with qbittorrent 5.3.0") {
		t.Fatalf("detail %q", p.Blockers()[0].Detail)
	}
	// Every consequential flag is explicit.
	var req map[string]bool
	_ = json.Unmarshal(p.Operations[0].Request, &req)
	for _, k := range []string{"removeFromClient", "blocklist", "skipRedownload", "changeCategory"} {
		if _, ok := req[k]; !ok {
			t.Errorf("flag %s not explicit", k)
		}
	}
	if req["removeFromClient"] || !req["blocklist"] || req["skipRedownload"] {
		t.Errorf("flags %v", req)
	}
	if len(p.Locks) != 2 || len(p.Effects) == 0 || p.Fingerprint == "" || !p.ExpiresAt.After(p.CreatedAt) {
		t.Errorf("proposal %+v", p)
	}
	// With a recorded tested workflow for exactly these versions, it is eligible.
	in := planInput()
	in.Tested = []TestedWorkflow{{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014",
		Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"}}
	if p, _ := Plan(in); !p.Eligible() {
		t.Fatalf("tested combination blocked by %v", blockerNames(p))
	}
	// A different client version is not covered.
	in.ClientProbe = &adapter.Probe{Version: "v5.3.1"}
	if p, _ := Plan(in); p.Eligible() {
		t.Fatal("untested version eligible")
	}
}

func TestPlanRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*PlanInput)
		want   string
	}{
		"private":         {func(in *PlanInput) { in.Transfer.Latest.Private = domain.KnownValue(true) }, "public transfer"},
		"unknown privacy": {func(in *PlanInput) { in.Transfer.Latest.Private = domain.UnknownValue[bool]("4.x") }, "public transfer"},
		"user paused":     {func(in *PlanInput) { in.Transfer.StateClass = domain.StatePaused }, "applicable transfer state"},
		"checking":        {func(in *PlanInput) { in.Transfer.StateClass = domain.StateChecking }, "applicable transfer state"},
		"queued":          {func(in *PlanInput) { in.Transfer.StateClass = domain.StateQueued }, "applicable transfer state"},
		"stale evidence":  {func(in *PlanInput) { in.Transfer.LastSeen = t0.Add(-time.Hour) }, "fresh evidence"},
		"not qualified":   {func(in *PlanInput) { in.Transfer.Assessment.Status = policy.StatusObserving }, "diagnosis qualified"},
		"no association":  {func(in *PlanInput) { in.Queue = nil }, "one manager association"},
		"ambiguous":       {func(in *PlanInput) { in.Queue = append(in.Queue, in.Queue[0]) }, "one manager association"},
		"transfer locked": {func(in *PlanInput) { in.Locks = map[string]int64{"transfer:qbit|aa": 4} }, "no conflicting action"},
		"content locked":  {func(in *PlanInput) { in.Locks = map[string]int64{"content:sonarr|episode:1": 4} }, "no conflicting action"},
		"cooldown": {func(in *PlanInput) {
			in.Cooldowns = map[string]store.CooldownRecord{"transfer:qbit|aa": {Until: t0.Add(time.Hour)}}
		}, "not in cooldown"},
		"content cooldown": {func(in *PlanInput) {
			in.Cooldowns = map[string]store.CooldownRecord{"content:sonarr|episode:1": {Until: t0.Add(time.Hour), Reason: "action #2"}}
		}, "not in cooldown"},
		"no content id": {func(in *PlanInput) { in.Queue[0].Latest.EpisodeID = 0 }, "content identity known"},
		"pack": {func(in *PlanInput) {
			second := in.Queue[0]
			second.QueueID, second.Latest.EpisodeID = 8, 2
			in.Queue = append(in.Queue, second)
		}, "one manager association"},
		"snoozed (auto)":   {func(in *PlanInput) { in.Finding.State = domain.FindingSnoozed }, "finding is active"},
		"resolved":         {func(in *PlanInput) { in.Finding.State = domain.FindingResolved }, "finding is active"},
		"observe mode":     {func(in *PlanInput) { in.Policy.Mode = "observe" }, "automation permitted by policy"},
		"not enabled":      {func(in *PlanInput) { in.Policy.Automation[0].Enabled = false }, "automation permitted by policy"},
		"manual (observe)": {func(in *PlanInput) { in.Origin, in.Policy.Mode = OriginManual, "observe" }, "manual action permitted by policy"},
		"manual excluded": {func(in *PlanInput) {
			in.Origin = OriginManual
			in.Policy.Exclusions = []policy.ExclusionMatch{{Workflows: []policy.Workflow{policy.WorkflowNoProgressRecovery}, Reason: "keep", From: "x"}}
		}, "manual action permitted by policy"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := planInput()
			in.Tested = []TestedWorkflow{{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "4.0.20.3014",
				Client: adapter.KindQBittorrent, ClientVersion: "5.3.0"}}
			c.mutate(&in)
			p, np := Plan(in)
			if np != nil {
				t.Fatal(np.Reason)
			}
			found := false
			for _, b := range blockerNames(p) {
				found = found || b == c.want
			}
			if !found || p.Eligible() {
				t.Fatalf("want blocker %q, got %v", c.want, blockerNames(p))
			}
		})
	}
	// A manual action on a snoozed finding is allowed (snooze suppresses automation).
	in := planInput()
	in.Origin, in.Finding.State = OriginManual, domain.FindingSnoozed
	p, _ := Plan(in)
	for _, b := range blockerNames(p) {
		if b == "finding is active" {
			t.Fatal("manual action on snoozed finding blocked")
		}
	}
}

func TestPlanNoWorkflow(t *testing.T) {
	in := planInput()
	in.Finding.Type = domain.FindingManagerHealth
	if p, np := Plan(in); p != nil || np == nil {
		t.Fatal("health finding produced a plan")
	}
}

// Review R6 (2026-11-16): replacement releases for one episode share its
// content lock, cooldown subject, and attempt allowance.
func TestContentIdentitySurvivesReplacement(t *testing.T) {
	a := planInput()
	b := planInput()
	b.Transfer.NativeID = "bb"
	b.Queue[0].DownloadID, b.Queue[0].Latest.DownloadID, b.Queue[0].QueueID = "bb", "bb", 8
	b.Queue[0].ItemKey = store.QueueItemKey(b.Queue[0].Latest)
	pa, _ := Plan(a)
	pb, _ := Plan(b)
	if pa.Locks[1] != "content:sonarr|episode:1" || pa.Locks[1] != pb.Locks[1] {
		t.Fatalf("content locks %v vs %v", pa.Locks, pb.Locks)
	}
	if pa.Budgets[2].Scope != "attempts:sonarr|episode:1" || pa.Budgets[2].Scope != pb.Budgets[2].Scope {
		t.Fatalf("attempt budgets %s vs %s", pa.Budgets[2].Scope, pb.Budgets[2].Scope)
	}
	if pa.Locks[0] == pb.Locks[0] {
		t.Fatal("distinct transfers share a transfer lock")
	}
	// A content lock held by the other release's action blocks this one.
	b.Locks = map[string]int64{pa.Locks[1]: 1}
	if p, _ := Plan(b); p.Eligible() || !strings.Contains(strings.Join(blockerNames(p), ","), "no conflicting action") {
		t.Fatalf("blockers %v", blockerNames(p))
	}
}

func TestParseContentKey(t *testing.T) {
	for _, k := range []string{"episode:1", "movie:42"} {
		if _, _, ok := domain.ParseContentKey(k); !ok {
			t.Errorf("%s rejected", k)
		}
	}
	for _, k := range []string{"", "episode:", "episode:0", "episode:01", "series:3", "aa|episode:1", "movie:-1", "movie:1x"} {
		if _, _, ok := domain.ParseContentKey(k); ok {
			t.Errorf("%s accepted", k)
		}
	}
}

func TestPlanUsesConfiguredLimits(t *testing.T) {
	in := planInput()
	p, _ := Plan(in)
	def := policy.DefaultLimits()
	if len(p.Budgets) != 3 || p.Budgets[0].Limit != def.Acquisitions || p.Budgets[2].Limit != def.ContentAttempts ||
		p.Cooldown != def.SuccessCooldown || p.Budgets[0].From != "built-in proposed default" {
		t.Fatalf("default budgets %+v cooldown %v", p.Budgets, p.Cooldown)
	}
	in.Limits = policy.Limits{Acquisitions: 50, AcquisitionWindow: 6 * time.Hour, ManagerAcquisitions: 10, ContentAttempts: 5, SuccessCooldown: time.Hour,
		Sources: []policy.Source{{Setting: "content_attempts", From: "config [automation.limits] content_attempts"}}}
	p, _ = Plan(in)
	if p.Budgets[0].Limit != 50 || p.Budgets[0].Window != 6*time.Hour || p.Budgets[1].Limit != 10 || p.Budgets[2].Limit != 5 ||
		p.Budgets[2].From != "config [automation.limits] content_attempts" || p.Cooldown != time.Hour {
		t.Fatalf("configured budgets %+v cooldown %v", p.Budgets, p.Cooldown)
	}
	// Exhaustion is a visible prerequisite, explained by kind.
	next := t0.Add(3 * time.Hour)
	p.CheckBudgets(map[string]store.BudgetUsage{
		"pacing:global":             {Used: 50, Limit: 50, NextAt: &next},
		"pacing:manager:sonarr":     {Used: 1, Limit: 10},
		"attempts:sonarr|episode:1": {Used: 5, Limit: 5},
	})
	var details []string
	for _, b := range p.Blockers() {
		details = append(details, b.Name+": "+b.Detail)
	}
	all := strings.Join(details, "\n")
	if !strings.Contains(all, "50 of 50 used in the last 6h") || !strings.Contains(all, "not a judgement about this item") ||
		!strings.Contains(all, "eligible again at "+next.Format(time.RFC3339)) || !strings.Contains(all, "5 of 5 recovery attempts used ever") ||
		!strings.Contains(all, "attempts reset sonarr|episode:1") || strings.Contains(all, "pacing, sonarr") {
		t.Fatalf("budget explanations:\n%s", all)
	}
}
