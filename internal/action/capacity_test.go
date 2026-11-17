package action

import (
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// R12: an already-reached retained ceiling stops every replacement,
// whether the transfer would be stopped or (private, keep-running) left
// active; separately, the transfer left behind must itself fit.
func TestRetainedCapacityGatesEveryReplacement(t *testing.T) {
	variants := map[string]func(*PlanInput){
		"public stop": func(*PlanInput) {},
		"private keep-running": func(in *PlanInput) {
			in.Transfer.Latest.Private = domain.KnownValue(true)
			in.Policy.PrivateRecovery = policy.PrivateKeepRunning
		},
		"unknown privacy keep-running": func(in *PlanInput) {
			in.Transfer.Latest.Private = domain.UnknownValue[bool]("not reported")
			in.Policy.PrivateRecovery = policy.PrivateKeepRunning
		},
		"private stop": func(in *PlanInput) {
			in.Transfer.Latest.Private = domain.KnownValue(true)
			in.Policy.PrivateRecovery = policy.PrivateStop
		},
	}
	const limitCount, limitBytes = 25, int64(100)
	cases := []struct {
		name       string
		bytes      bool // configure a byte ceiling of limitBytes
		usage      store.RetainedUsage
		size       *int64 // this transfer's wanted size; nil = unknown
		gate, room bool
	}{
		{name: "below the count ceiling", usage: store.RetainedUsage{Count: limitCount - 1}, gate: true, room: true},
		{name: "at the count ceiling", usage: store.RetainedUsage{Count: limitCount}, gate: false, room: false},
		{name: "over the count ceiling", usage: store.RetainedUsage{Count: limitCount + 3}, gate: false, room: false},
		{name: "unknown size without a byte ceiling", usage: store.RetainedUsage{Count: 1, UnknownSize: 1}, gate: true, room: true},
		{name: "bytes fit exactly", bytes: true, usage: store.RetainedUsage{Count: 1, KnownBytes: 99}, size: ptr(1), gate: true, room: true},
		{name: "bytes one over", bytes: true, usage: store.RetainedUsage{Count: 1, KnownBytes: 99}, size: ptr(2), gate: true, room: false},
		{name: "at the byte ceiling", bytes: true, usage: store.RetainedUsage{Count: 1, KnownBytes: 100}, size: ptr(0), gate: false, room: true},
		{name: "retained of unknown size", bytes: true, usage: store.RetainedUsage{Count: 1, UnknownSize: 1}, size: ptr(1), gate: false, room: true},
		{name: "no-payload records leave room", bytes: true, usage: store.RetainedUsage{Count: 2, NoPayload: 2}, size: ptr(1), gate: true, room: true},
		{name: "this transfer of unknown size", bytes: true, usage: store.RetainedUsage{}, size: nil, gate: true, room: false},
	}
	for vname, variant := range variants {
		for _, tc := range cases {
			t.Run(vname+"/"+tc.name, func(t *testing.T) {
				in := planInput()
				variant(&in)
				in.Limits = policy.DefaultLimits()
				in.Limits.RetainedTransfers = limitCount
				if tc.bytes {
					in.Limits.RetainedBytes = limitBytes
				}
				in.Retained = tc.usage
				if tc.size != nil {
					in.Transfer.Latest.WantedBytes = domain.KnownValue(*tc.size)
				}
				p, _ := Plan(in)
				if !hasOp(p, "manager.queue.fail") {
					t.Fatalf("variant does not request a replacement: %+v", p.Operations)
				}
				g, r := find(p.Prerequisites, "retained-data ceiling"), find(p.Prerequisites, "room to retain this transfer")
				if g == nil || r == nil {
					t.Fatalf("capacity checks missing: %v", blockerNames(p))
				}
				if g.Satisfied != tc.gate || r.Satisfied != tc.room || !g.Hard || !r.Hard {
					t.Fatalf("gate %v (want %v): %s\nroom %v (want %v): %s", g.Satisfied, tc.gate, g.Detail, r.Satisfied, tc.room, r.Detail)
				}
				if !tc.gate && !strings.Contains(g.Detail, "No replacement is requested") {
					t.Fatalf("gate detail is not actionable: %s", g.Detail)
				}
			})
		}
	}
}

// R13: the shared preview states what this build does track, without
// promising a search result or measuring disk use.
func TestPreviewDescribesCurrentTracking(t *testing.T) {
	in := planInput()
	in.Limits = policy.DefaultLimits()
	p, _ := Plan(in)
	all := strings.Join(p.SideEffects, " | ")
	for _, stale := range []string{"does not yet count", "not yet tracked"} {
		if strings.Contains(all, stale) {
			t.Fatalf("stale claim %q in %s", stale, all)
		}
	}
	for _, want := range []string{"counted as retained", "replacement grab", "does not mean a release will be found"} {
		if !strings.Contains(all, want) {
			t.Fatalf("side effects lack %q: %s", want, all)
		}
	}
	g := find(p.Prerequisites, "retained-data ceiling")
	if g == nil || !strings.Contains(g.Detail, "not disk use") || !strings.Contains(g.Detail, "no free-space threshold is enforced") {
		t.Fatalf("capacity detail overstates measurement: %+v", g)
	}
}

func ptr(v int64) *int64 { return &v }
