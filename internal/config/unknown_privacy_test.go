package config

import (
	"testing"

	"github.com/voidxela/housekeeparr/internal/policy"
)

func TestUnknownPrivacyOptInValidation(t *testing.T) {
	expectProblem(t, minimal+"\n[automation.unknown_privacy]\nno_progress_recovery = true\n",
		"automation.unknown_privacy.no_progress_recovery", "only for metadata_recovery")
	expectProblem(t, minimal+"\n[automation.unknown_privacy]\nbogus = true\n", "automation.unknown_privacy.bogus", "unknown workflow")
	expectProblem(t, minimal+`
[[rule]]
name = "a"
priority = 1
[rule.match]
client = "qbit"
[rule.unknown_privacy]
metadata_recovery = true

[[rule]]
name = "b"
priority = 1
[rule.match]
client = "qbit"
[rule.unknown_privacy]
metadata_recovery = false
`, "rule", "unknown_privacy.metadata_recovery")
}

// Allowed by default (no payload exists before metadata); a rule can refuse
// it for one category, with provenance; the effective revision changes
// only where the refusal applies, and the diagnostic settings revision
// never does.
func TestUnknownPrivacyOptInResolution(t *testing.T) {
	base := mustParse(t, minimal)
	def, err := base.Resolve(policy.Subject{ClientID: "qbit", Category: "tv", Privacy: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, from := def.AllowsUnknownPrivacy(policy.WorkflowMetadataRecovery); !ok || from != "built-in default (no payload exists before metadata)" {
		t.Fatalf("default: %v %q", ok, from)
	}
	cfg := mustParse(t, minimal+`
[[rule]]
name = "private trackers"
priority = 5
[rule.match]
client = "qbit"
category = "tv"
privacy = "unknown"
[rule.unknown_privacy]
metadata_recovery = false
`)
	in, err := cfg.Resolve(policy.Subject{ClientID: "qbit", Category: "tv", Privacy: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, from := in.AllowsUnknownPrivacy(policy.WorkflowMetadataRecovery); ok || from != `config rule "private trackers" (priority 5)` {
		t.Fatalf("rule: %v %q", ok, from)
	}
	if ok, _ := in.AllowsUnknownPrivacy(policy.WorkflowNoProgressRecovery); ok {
		t.Fatal("opt-in leaked to no-progress recovery")
	}
	out, err := cfg.Resolve(policy.Subject{ClientID: "qbit", Category: "movies", Privacy: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := out.AllowsUnknownPrivacy(policy.WorkflowMetadataRecovery); !ok {
		t.Fatal("the refusal applied outside the rule's category")
	}
	if out.Revision != def.Revision {
		t.Fatal("a non-matching refusal changed the effective revision")
	}
	if in.Revision == def.Revision {
		t.Fatal("a refusal did not change the effective revision (the per-write gate would miss it)")
	}
	if in.Settings.Revision() != def.Settings.Revision() {
		t.Fatal("the setting changed the diagnostic settings revision (it would force requalification)")
	}
}
