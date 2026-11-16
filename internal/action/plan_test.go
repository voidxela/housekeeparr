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
		Latest: domain.QueueItem{Title: "Show.S01E01"}}
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
		"private":          {func(in *PlanInput) { in.Transfer.Latest.Private = domain.KnownValue(true) }, "public transfer"},
		"unknown privacy":  {func(in *PlanInput) { in.Transfer.Latest.Private = domain.UnknownValue[bool]("4.x") }, "public transfer"},
		"user paused":      {func(in *PlanInput) { in.Transfer.StateClass = domain.StatePaused }, "applicable transfer state"},
		"checking":         {func(in *PlanInput) { in.Transfer.StateClass = domain.StateChecking }, "applicable transfer state"},
		"queued":           {func(in *PlanInput) { in.Transfer.StateClass = domain.StateQueued }, "applicable transfer state"},
		"stale evidence":   {func(in *PlanInput) { in.Transfer.LastSeen = t0.Add(-time.Hour) }, "fresh evidence"},
		"not qualified":    {func(in *PlanInput) { in.Transfer.Assessment.Status = policy.StatusObserving }, "diagnosis qualified"},
		"no association":   {func(in *PlanInput) { in.Queue = nil }, "one manager association"},
		"ambiguous":        {func(in *PlanInput) { in.Queue = append(in.Queue, in.Queue[0]) }, "one manager association"},
		"locked":           {func(in *PlanInput) { in.LockHolder = 4 }, "no conflicting action"},
		"cooldown":         {func(in *PlanInput) { u := t0.Add(time.Hour); in.CooldownUntil = &u }, "not in cooldown"},
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
