package action

import (
	"slices"
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
)

// metadataInput is a qualified metadata timeout on a magnet whose privacy
// the client cannot report yet.
func metadataInput() PlanInput {
	in := planInput()
	in.Finding.Type = domain.FindingMetadataTimeout
	in.Transfer.StateClass = domain.StateMetadata
	in.Transfer.Latest.NativeState = "metaDL"
	in.Transfer.Latest.Private = domain.UnknownValue[bool]("unknown until metadata is acquired")
	in.Transfer.Latest.WantedBytes = domain.UnknownValue[int64]("unavailable until metadata is acquired")
	in.Transfer.Latest.VerifiedBytes = domain.KnownValue[int64](0)
	in.Transfer.Assessment.Diagnosis = policy.DiagnosisMetadataTimeout
	in.Policy.Automation = []policy.WorkflowPermission{{Workflow: policy.WorkflowMetadataRecovery, Enabled: true, From: "test"}}
	in.ClientProbe.Read = adapter.Capabilities{adapter.CapMetadataState: {Support: adapter.Supported, Note: "metaDL"}}
	return in
}

func optIn(in *PlanInput) {
	in.Policy.UnknownPrivacy = []policy.WorkflowPermission{{Workflow: policy.WorkflowMetadataRecovery, Enabled: true, From: "config rule \"public magnets\""}}
}

// A scope that turned unknown privacy off (or a policy that does not state
// it at all) refuses, naming the setting.
func TestMetadataRecoveryRefusesUnknownPrivacyWhenTurnedOff(t *testing.T) {
	in := metadataInput()
	in.Policy.UnknownPrivacy = []policy.WorkflowPermission{{Workflow: policy.WorkflowMetadataRecovery, Enabled: false, From: "config rule \"private trackers\""}}
	p, np := Plan(in)
	if np != nil {
		t.Fatal(np.Reason)
	}
	b := blockerNames(p)
	if !slices.Equal(b, []string{"tested write workflow", "public transfer"}) {
		t.Fatalf("blockers %v", b)
	}
	for _, c := range p.Blockers() {
		if c.Name == "public transfer" && !strings.Contains(c.Detail, "unknown_privacy.metadata_recovery") {
			t.Fatalf("refusal does not name the opt-in: %q", c.Detail)
		}
	}
	if p.UnknownPrivacy {
		t.Fatal("marked as allowed")
	}
	bare, _ := Plan(metadataInput())
	if c := find(bare.Prerequisites, "public transfer"); c == nil || c.Satisfied {
		t.Fatalf("a policy that does not state the setting must fail closed: %+v", c)
	}
}

func TestMetadataRecoveryWithUnknownPrivacyOptIn(t *testing.T) {
	in := metadataInput()
	optIn(&in)
	p, _ := Plan(in)
	if b := blockerNames(p); !slices.Equal(b, []string{"tested write workflow"}) {
		t.Fatalf("blockers %v", b)
	}
	if !p.UnknownPrivacy {
		t.Fatal("opt-in not recorded on the proposal")
	}
	if !slices.ContainsFunc(p.Effects, func(e string) bool { return strings.Contains(e, "no payload data") }) {
		t.Fatalf("effects do not say there is no payload: %v", p.Effects)
	}
	// Live revalidation accepts privacy that is still unknown, never a
	// transfer that turned out to be private, and requires the state to
	// still be metadata acquisition.
	live := matching(*p, false)
	if c := find(Revalidate(*p, 0, live), "public transfer"); c == nil || !c.Satisfied {
		t.Fatalf("still unknown: %+v", c)
	}
	live.Transfer.Private = domain.KnownValue(true)
	if c := find(Revalidate(*p, 0, live), "public transfer"); c == nil || c.Satisfied {
		t.Fatalf("now private: %+v", c)
	}
	live.Transfer.Private, live.Transfer.StateClass = in.Transfer.Latest.Private, domain.StateDownloading
	if c := find(Revalidate(*p, 0, live), "transfer state unchanged"); c == nil || c.Satisfied {
		t.Fatalf("metadata arrived: %+v", c)
	}
}

func TestMetadataRecoveryRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*PlanInput)
		want   string
	}{
		"known private despite opt-in": {func(in *PlanInput) { in.Transfer.Latest.Private = domain.KnownValue(true) }, "public transfer"},
		"capability not reported":      {func(in *PlanInput) { in.ClientProbe.Read = nil }, "metadata state supported"},
		"no probe":                     {func(in *PlanInput) { in.ClientProbe = nil }, "metadata state supported"},
		"capability unsupported": {func(in *PlanInput) {
			in.ClientProbe.Read[adapter.CapMetadataState] = adapter.CapabilityStatus{Support: adapter.NotSupported}
		}, "metadata state supported"},
		"no-progress diagnosis": {func(in *PlanInput) { in.Transfer.Assessment.Diagnosis = policy.DiagnosisNoProgress }, "diagnosis matches workflow"},
		"not in metadata state": {func(in *PlanInput) { in.Transfer.StateClass = domain.StateDownloading }, "applicable transfer state"},
		"only no-progress automation enabled": {func(in *PlanInput) {
			in.Policy.Automation = []policy.WorkflowPermission{{Workflow: policy.WorkflowNoProgressRecovery, Enabled: true, From: "test"}}
		}, "automation permitted"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := metadataInput()
			optIn(&in)
			tc.mutate(&in)
			p, _ := Plan(in)
			if !slices.ContainsFunc(blockerNames(p), func(b string) bool { return strings.HasPrefix(b, tc.want) }) {
				t.Fatalf("want blocker %q, got %v", tc.want, blockerNames(p))
			}
		})
	}
}

// A metadata-stage transfer has no payload, so stopping it does not trip a
// byte ceiling; its logical size is still not invented (recorded unknown,
// with zero verified payload, and refreshed if it is resumed).
func TestMetadataRecoveryRetainsZeroBytes(t *testing.T) {
	in := metadataInput()
	optIn(&in)
	in.Limits = policy.DefaultLimits()
	in.Limits.RetainedBytes = 1 << 30
	p, _ := Plan(in)
	for _, name := range []string{"retained-data ceiling", "room to retain this transfer"} {
		if c := find(p.Prerequisites, name); c == nil || !c.Satisfied {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	if v, ok := p.Planned.Wanted.Get(); ok {
		t.Fatalf("planned wanted size invented: %d", v)
	}
}

// The setting exists only for the workflows that define it: a forged entry
// for another workflow is ignored.
func TestUnknownPrivacySettingIsLimitedToDefinedWorkflows(t *testing.T) {
	e := policy.Effective{UnknownPrivacy: []policy.WorkflowPermission{{Workflow: policy.WorkflowPartialPack, Enabled: true, From: "forged"}}}
	if ok, _ := e.AllowsUnknownPrivacy(policy.WorkflowPreservedDataFailure); ok {
		t.Fatal("unknown privacy accepted for a workflow that does not define it")
	}
}

func find(cs []Check, name string) *Check {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}
