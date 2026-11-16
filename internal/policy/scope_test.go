package policy

import (
	"slices"
	"testing"
	"time"
)

func TestAllWorkflowsAndValidWorkflow(t *testing.T) {
	want := []Workflow{
		WorkflowNoProgressRecovery,
		WorkflowMetadataRecovery,
		WorkflowPreservedDataFailure,
		WorkflowPartialPack,
		WorkflowMissingSearch,
	}
	got := AllWorkflows()
	if !slices.Equal(got, want) {
		t.Fatalf("AllWorkflows() = %v, want %v", got, want)
	}
	for _, wf := range got {
		if !ValidWorkflow(wf) {
			t.Errorf("ValidWorkflow(%q) = false, want true", wf)
		}
	}
	for _, bad := range []Workflow{"", "unknown", "stall_recovery"} {
		if ValidWorkflow(bad) {
			t.Errorf("ValidWorkflow(%q) = true, want false", bad)
		}
	}
}

func TestEffectivePermits(t *testing.T) {
	allFalse := func() []WorkflowPermission {
		var out []WorkflowPermission
		for _, wf := range AllWorkflows() {
			out = append(out, WorkflowPermission{Workflow: wf, Enabled: false, From: "built-in default"})
		}
		return out
	}
	withEnabled := func(wfs ...Workflow) []WorkflowPermission {
		out := allFalse()
		for i := range out {
			if slices.Contains(wfs, out[i].Workflow) {
				out[i].Enabled = true
				out[i].From = "config [automation.workflows]"
			}
		}
		return out
	}

	tests := []struct {
		name     string
		eff      Effective
		workflow Workflow
		wantOK   bool
		wantMsgs []string
	}{
		{
			name: "permitted when automatic, no stop, enabled, no exclusion",
			eff: Effective{
				Mode:       "automatic",
				Automation: withEnabled(WorkflowNoProgressRecovery),
			},
			workflow: WorkflowNoProgressRecovery,
			wantOK:   true,
		},
		{
			name: "enabling one workflow does not enable another",
			eff: Effective{
				Mode:       "automatic",
				Automation: withEnabled(WorkflowNoProgressRecovery),
			},
			workflow: WorkflowMetadataRecovery,
			wantOK:   false,
			wantMsgs: []string{"metadata_recovery is not enabled (built-in default)"},
		},
		{
			name: "mode observe blocks",
			eff: Effective{
				Mode:       "observe",
				Automation: withEnabled(WorkflowNoProgressRecovery),
			},
			workflow: WorkflowNoProgressRecovery,
			wantOK:   false,
			wantMsgs: []string{"mode is observe"},
		},
		{
			name: "mode paused blocks",
			eff: Effective{
				Mode:       "paused",
				Automation: withEnabled(WorkflowNoProgressRecovery),
			},
			workflow: WorkflowNoProgressRecovery,
			wantOK:   false,
			wantMsgs: []string{"mode is paused"},
		},
		{
			name: "emergency stop blocks",
			eff: Effective{
				Mode:          "automatic",
				EmergencyStop: true,
				Automation:    withEnabled(WorkflowNoProgressRecovery),
			},
			workflow: WorkflowNoProgressRecovery,
			wantOK:   false,
			wantMsgs: []string{"emergency stop is set"},
		},
		{
			name: "exclusion for another workflow does not block",
			eff: Effective{
				Mode:       "automatic",
				Automation: withEnabled(WorkflowNoProgressRecovery),
				Exclusions: []ExclusionMatch{
					{Workflows: []Workflow{WorkflowMissingSearch}, Reason: "manual search only", From: "config exclude #1"},
				},
			},
			workflow: WorkflowNoProgressRecovery,
			wantOK:   true,
		},
		{
			name: "exclusion blocks with reason and origin",
			eff: Effective{
				Mode:       "automatic",
				Automation: withEnabled(WorkflowNoProgressRecovery),
				Exclusions: []ExclusionMatch{
					{Workflows: []Workflow{WorkflowNoProgressRecovery, WorkflowMetadataRecovery}, Reason: "seedbox tag", From: "config exclude #2"},
				},
			},
			workflow: WorkflowNoProgressRecovery,
			wantOK:   false,
			wantMsgs: []string{"excluded: seedbox tag (config exclude #2)"},
		},
		{
			name: "all blockers reported together",
			eff: Effective{
				Mode:          "observe",
				EmergencyStop: true,
				Automation:    allFalse(),
				Exclusions: []ExclusionMatch{
					{Workflows: []Workflow{WorkflowNoProgressRecovery}, Reason: "keep", From: "config exclude #2"},
				},
			},
			workflow: WorkflowNoProgressRecovery,
			wantOK:   false,
			wantMsgs: []string{
				"mode is observe",
				"emergency stop is set",
				"no_progress_recovery is not enabled (built-in default)",
				"excluded: keep (config exclude #2)",
			},
		},
		{
			name: "missing automation list and unset mode",
			eff: Effective{
				Mode: "",
			},
			workflow: WorkflowPartialPack,
			wantOK:   false,
			wantMsgs: []string{
				"mode is unset",
				"partial_pack is not enabled (not configured)",
			},
		},
		{
			name: "unknown workflow rejected",
			eff: Effective{
				Mode:       "automatic",
				Automation: withEnabled(WorkflowNoProgressRecovery),
			},
			workflow: Workflow("bogus"),
			wantOK:   false,
			wantMsgs: []string{`unknown workflow "bogus"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, reasons := tc.eff.Permits(tc.workflow)
			if ok != tc.wantOK {
				t.Fatalf("Permits(%q) ok = %v, want %v (reasons=%v)", tc.workflow, ok, tc.wantOK, reasons)
			}
			if !slices.Equal(reasons, tc.wantMsgs) {
				t.Fatalf("Permits(%q) reasons = %#v, want %#v", tc.workflow, reasons, tc.wantMsgs)
			}
		})
	}
}

func TestEffectiveComputeRevision(t *testing.T) {
	base := Effective{
		Mode:          "observe",
		EmergencyStop: false,
		Settings:      DefaultSettings(),
		Automation: []WorkflowPermission{
			{Workflow: WorkflowNoProgressRecovery, Enabled: false, From: "built-in default"},
			{Workflow: WorkflowMetadataRecovery, Enabled: false, From: "built-in default"},
		},
		Exclusions: []ExclusionMatch{
			{Workflows: []Workflow{WorkflowMissingSearch, WorkflowPartialPack}, Reason: "r1", From: "config exclude #1"},
		},
	}
	baseRev := base.ComputeRevision()
	if len(baseRev) != 12 {
		t.Fatalf("revision length = %d, want 12", len(baseRev))
	}

	// Order-independent across Automation and ExclusionMatch.Workflows ordering.
	reordered := base
	reordered.Automation = []WorkflowPermission{
		{Workflow: WorkflowMetadataRecovery, Enabled: false, From: "built-in default"},
		{Workflow: WorkflowNoProgressRecovery, Enabled: false, From: "built-in default"},
	}
	reordered.Exclusions = []ExclusionMatch{
		{Workflows: []Workflow{WorkflowPartialPack, WorkflowMissingSearch}, Reason: "r1", From: "config exclude #1"},
	}
	if got := reordered.ComputeRevision(); got != baseRev {
		t.Errorf("reordered revision = %s, want %s", got, baseRev)
	}

	mutations := []struct {
		name string
		edit func(*Effective)
	}{
		{"mode", func(e *Effective) { e.Mode = "automatic" }},
		{"emergency_stop", func(e *Effective) { e.EmergencyStop = true }},
		{"setting", func(e *Effective) { e.Settings.NoProgressWindow = 3 * time.Hour }},
		{"permission", func(e *Effective) {
			e.Automation = []WorkflowPermission{
				{Workflow: WorkflowNoProgressRecovery, Enabled: true, From: "config [automation.workflows]"},
				{Workflow: WorkflowMetadataRecovery, Enabled: false, From: "built-in default"},
			}
		}},
		{"exclusion_added", func(e *Effective) {
			e.Exclusions = append(slices.Clone(e.Exclusions), ExclusionMatch{
				Workflows: []Workflow{WorkflowNoProgressRecovery},
				Reason:    "r2",
				From:      "config exclude #2",
			})
		}},
		{"exclusion_reason", func(e *Effective) {
			e.Exclusions = []ExclusionMatch{
				{Workflows: []Workflow{WorkflowMissingSearch, WorkflowPartialPack}, Reason: "different", From: "config exclude #1"},
			}
		}},
	}

	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			cp := base
			m.edit(&cp)
			if got := cp.ComputeRevision(); got == baseRev {
				t.Errorf("revision did not change after mutating %s", m.name)
			}
		})
	}
}
