package action

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
)

func hasOp(p *Proposal, op string) bool {
	return slices.ContainsFunc(p.Operations, func(o Operation) bool { return o.Op == op })
}

// No-progress recovery of a private (or possibly private) torrent: by
// default the grab is failed and blocklisted and the torrent is left
// running; stopping it too, or not acting at all, are scoped choices.
func TestPrivateNoProgressRecoveryModes(t *testing.T) {
	privacies := map[string]domain.Signal[bool]{"private": domain.KnownValue(true)}
	for pname, priv := range privacies {
		for _, tc := range []struct {
			mode           policy.PrivateMode
			eligible, stop bool
		}{
			{policy.PrivateKeepRunning, true, false},
			{policy.PrivateStop, true, true},
			{policy.PrivateObserve, false, false},
			{"", false, false}, // a policy that does not state it fails closed
		} {
			t.Run(pname+"/"+string(tc.mode), func(t *testing.T) {
				in := planInput()
				in.Transfer.Latest.Private = priv
				in.Policy.PrivateRecovery, in.Policy.PrivateRecoveryFrom = tc.mode, "test"
				p, _ := Plan(in)
				c := find(p.Prerequisites, "public transfer")
				if c == nil || c.Satisfied != tc.eligible || c.Detail == "" {
					t.Fatalf("privacy check %+v", c)
				}
				if !tc.eligible {
					if p.PrivateMode != "" {
						t.Fatal("refused plan records a private mode")
					}
					return
				}
				if p.PrivateMode != tc.mode || !hasOp(p, "manager.queue.fail") || hasOp(p, "client.stop") != tc.stop {
					t.Fatalf("mode %q ops %+v", p.PrivateMode, p.Operations)
				}
				if !tc.stop {
					if c := find(p.Prerequisites, "room to retain this transfer"); c == nil || !c.Satisfied || !strings.Contains(c.Detail, "kept running") {
						t.Fatalf("a torrent left running is not counted as retained: %+v", c)
					}
					if !slices.ContainsFunc(p.Effects, func(e string) bool { return strings.Contains(e, "not stopped") }) {
						t.Fatalf("effects do not say the torrent keeps running: %v", p.Effects)
					}
				}
				// Before the write the transfer is still private: accepted.
				if lc := find(Revalidate(*p, 0, matching(*p, false)), "public transfer"); lc == nil || !lc.Satisfied {
					t.Fatalf("live: %+v", lc)
				}
			})
		}
	}
}

// Unknown privacy is treated as private by default, so it follows the
// private mode; a scope can turn that off, and a policy that does not state
// it fails closed.
func TestUnknownPrivacyNoProgressFollowsPrivateMode(t *testing.T) {
	unknown := domain.UnknownValue[bool]("not reported by 4.x")
	on := []policy.WorkflowPermission{{Workflow: policy.WorkflowNoProgressRecovery, Enabled: true, From: "built-in default"}}
	off := []policy.WorkflowPermission{{Workflow: policy.WorkflowNoProgressRecovery, Enabled: false, From: "config rule \"cautious\""}}
	for _, tc := range []struct {
		name           string
		perm           []policy.WorkflowPermission
		mode           policy.PrivateMode
		eligible, stop bool
	}{
		{"default", on, policy.PrivateKeepRunning, true, false},
		{"stop", on, policy.PrivateStop, true, true},
		{"observe", on, policy.PrivateObserve, false, false},
		{"turned off", off, policy.PrivateKeepRunning, false, false},
		{"not stated", nil, policy.PrivateKeepRunning, false, false},
		{"mode not stated", on, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := planInput()
			in.Transfer.Latest.Private = unknown
			in.Policy.UnknownPrivacy, in.Policy.PrivateRecovery = tc.perm, tc.mode
			p, _ := Plan(in)
			c := find(p.Prerequisites, "public transfer")
			if c == nil || c.Satisfied != tc.eligible || c.Detail == "" {
				t.Fatalf("privacy check %+v", c)
			}
			if tc.eligible && (p.PrivateMode != tc.mode || hasOp(p, "client.stop") != tc.stop || !strings.Contains(c.Detail, "treated as private")) {
				t.Fatalf("mode %q ops %+v detail %q", p.PrivateMode, p.Operations, c.Detail)
			}
			if !tc.eligible && p.PrivateMode != "" {
				t.Fatal("refused plan records a private mode")
			}
		})
	}
}

// A public torrent is unaffected by the private mode, and the mode does
// not extend to other workflows.
func TestPrivateModeScope(t *testing.T) {
	in := planInput()
	in.Policy.PrivateRecovery = policy.PrivateObserve
	p, _ := Plan(in)
	if c := find(p.Prerequisites, "public transfer"); !c.Satisfied || !hasOp(p, "client.stop") || p.PrivateMode != "" {
		t.Fatalf("public transfer changed by the private mode: %+v", c)
	}
	in = metadataInput()
	optIn(&in)
	in.Transfer.Latest.Private = domain.KnownValue(true)
	in.Policy.PrivateRecovery = policy.PrivateKeepRunning
	p, _ = Plan(in)
	if c := find(p.Prerequisites, "public transfer"); c.Satisfied {
		t.Fatalf("private_recovery applied to metadata recovery: %+v", c)
	}
}

// A private torrent its tracker rejects as not registered follows
// private_recovery (user decision 2026-11-17), and so does one of unknown
// privacy unless the scope turned unknown_privacy.preserved_data_failure off.
func TestPreservedDataFailureFollowsPrivateRecovery(t *testing.T) {
	base := func() PlanInput {
		in := planInput()
		in.Finding.Type = domain.FindingSourceRejected
		in.Transfer.Assessment.Diagnosis = policy.DiagnosisSourceUnregistered
		in.Policy.Automation = []policy.WorkflowPermission{{Workflow: policy.WorkflowPreservedDataFailure, Enabled: true, From: "test"}}
		in.Queue[0].Latest.TrackedState = domain.KnownValue("downloading")
		return in
	}
	for _, tc := range []struct {
		mode           policy.PrivateMode
		eligible, stop bool
	}{{policy.PrivateKeepRunning, true, false}, {policy.PrivateStop, true, true}, {policy.PrivateObserve, false, false}} {
		t.Run(string(tc.mode), func(t *testing.T) {
			in := base()
			in.Transfer.Latest.Private = domain.KnownValue(true)
			in.Policy.PrivateRecovery, in.Policy.PrivateRecoveryFrom = tc.mode, "test"
			p, _ := Plan(in)
			c := find(p.Prerequisites, "public transfer")
			if c == nil || c.Satisfied != tc.eligible {
				t.Fatalf("privacy check %+v", c)
			}
			if tc.eligible && (p.PrivateMode != tc.mode || !hasOp(p, "manager.queue.fail") || hasOp(p, "client.stop") != tc.stop) {
				t.Fatalf("mode %q ops %+v", p.PrivateMode, p.Operations)
			}
		})
	}
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprintf("unknown privacy, allowed=%v", allowed), func(t *testing.T) {
			in := base()
			in.Transfer.Latest.Private = domain.UnknownValue[bool]("not reported")
			in.Policy.PrivateRecovery = policy.PrivateKeepRunning
			in.Policy.UnknownPrivacy = []policy.WorkflowPermission{{Workflow: policy.WorkflowPreservedDataFailure, Enabled: allowed, From: "test"}}
			p, _ := Plan(in)
			c := find(p.Prerequisites, "public transfer")
			if c == nil || c.Satisfied != allowed || (p.PrivateMode == policy.PrivateKeepRunning) != allowed || (allowed && hasOp(p, "client.stop")) {
				t.Fatalf("privacy check %+v mode %q", c, p.PrivateMode)
			}
		})
	}
}
