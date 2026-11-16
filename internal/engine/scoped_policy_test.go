package engine

import (
	"testing"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/policy"
)

// Diagnosis must use each transfer's own effective policy. Before this was
// fixed, the engine diagnosed every transfer with its client's policy, so an
// item override documented as "wait longer before failing" did not delay
// the diagnosis (or the recovery planned from it) at all.
func TestDiagnosisUsesScopedRulesAndOverrides(t *testing.T) {
	const (
		hOverrideLong = "1111111111111111111111111111111111111111"
		hPlain        = "2222222222222222222222222222222222222222"
		hContentLong  = "3333333333333333333333333333333333333333"
		hRuleLong     = "4444444444444444444444444444444444444444"
		hOverrideFast = "5555555555555555555555555555555555555555"
	)
	cfg := baseConfig + `
[[rule]]
name = "rare category"
priority = 10
[rule.match]
client = "qbit"
category = "rare"
[rule.policy]
no_progress_window = "24h"

[[override]]
transfer = "qbit|` + hOverrideLong + `"
reason = "rare release, wait longer before failing"
[override.policy]
no_progress_window = "24h"

[[override]]
content = "sonarr|episode:103"
reason = "rare episode"
[override.policy]
no_progress_window = "24h"

[[override]]
transfer = "qbit|` + hOverrideFast + `"
reason = "fail fast"
[override.policy]
new_download_grace = "0s"
no_progress_window = "30m"
`
	h := newHarness(t, cfg)
	rare := fake.Transfer(hRuleLong, "Example.Show.S01E04", domain.StateDownloading, 2*gib, 10*gib, t0)
	rare.Category = domain.KnownValue("rare")
	h.client.SetTransfers(
		fake.Transfer(hOverrideLong, "Example.Show.S01E01", domain.StateDownloading, 2*gib, 10*gib, t0),
		fake.Transfer(hPlain, "Example.Show.S01E02", domain.StateDownloading, 2*gib, 10*gib, t0),
		fake.Transfer(hContentLong, "Example.Show.S01E03", domain.StateDownloading, 2*gib, 10*gib, t0),
		rare,
		fake.Transfer(hOverrideFast, "Example.Show.S01E05", domain.StateDownloading, 2*gib, 10*gib, t0),
	)
	h.mgr.SetQueue(
		fake.Episode(1, hOverrideLong, "Example.Show.S01E01", "Example Show", 1, 1),
		fake.Episode(2, hPlain, "Example.Show.S01E02", "Example Show", 1, 2),
		fake.Episode(3, hContentLong, "Example.Show.S01E03", "Example Show", 1, 3),
		fake.Episode(4, hRuleLong, "Example.Show.S01E04", "Example Show", 1, 4),
		fake.Episode(5, hOverrideFast, "Example.Show.S01E05", "Example Show", 1, 5),
	)
	stalled := func() map[string]domain.Finding {
		out := map[string]domain.Finding{}
		for _, f := range h.active(domain.FindingTransferStalled) {
			out[f.Subject.NativeID] = f
		}
		return out
	}
	h.cycle()

	// A shorter override qualifies first; the defaults (30m grace + 90m
	// window) are not yet met for anything else.
	h.run(50, 10)
	got := stalled()
	if _, ok := got[hOverrideFast]; !ok || len(got) != 1 {
		t.Fatalf("after 50m: stalled %v, want only the fail-fast override", keys(got))
	}

	// Past the default window, the plain transfer qualifies too; the
	// longer transfer override, content override, and category rule keep
	// theirs unqualified.
	h.run(130, 10)
	got = stalled()
	for _, want := range []string{hPlain, hOverrideFast} {
		if _, ok := got[want]; !ok {
			t.Errorf("after 3h: %s not stalled (stalled: %v)", want, keys(got))
		}
	}
	for _, not := range []string{hOverrideLong, hContentLong, hRuleLong} {
		if _, ok := got[not]; ok {
			t.Errorf("after 3h: %s diagnosed despite its 24h window", not)
		}
		rec, err := h.st.Transfer(ctx, "qbit", not)
		if err != nil || rec == nil || rec.Assessment == nil {
			t.Fatalf("%s: no assessment: %v", not, err)
		}
		if rec.Assessment.Status == policy.StatusQualified {
			t.Errorf("%s: assessment qualified despite its 24h window: %+v", not, rec.Assessment)
		}
	}
}

func keys(m map[string]domain.Finding) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
