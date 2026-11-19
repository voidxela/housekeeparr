package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/store"
)

// The default timing boundaries of the worker loop, deterministically: a
// dead grab added at t0 under 10-minute polling, 30-minute grace, the
// 90-minute window and three observations is recovered at the first cycle
// at or after t0+120m, never earlier. A missed poll (a 20-minute gap, the
// default maximum) keeps the streak but counts only one nominal interval,
// so recovery moves one cycle later; a longer outage restarts
// qualification from the first observation after it.
func TestDefaultTimingBoundaries(t *testing.T) {
	cases := []struct {
		name string
		gap  func(w *world) // applied at t0+50m, instead of the 60m cycle
		want time.Duration  // first recovery, after t0
	}{
		{"every poll", nil, 120 * time.Minute},
		{"one missed poll", func(w *world) { w.clock.Advance(10 * time.Minute) }, 130 * time.Minute},
		{"30-minute outage", func(w *world) { w.clock.Advance(20 * time.Minute) }, 170 * time.Minute}, // streak restarts at t0+80m
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, "automatic", 20)
			w.stalled(hashA, 1, 1)
			w.run(50 * time.Minute)
			if tc.gap != nil {
				tc.gap(w)
			}
			for len(w.client.Stops()) == 0 {
				if w.clock.Now().Sub(t0) > 4*time.Hour {
					t.Fatal("never recovered")
				}
				w.run(10 * time.Minute)
			}
			if got := w.clock.Now().Sub(t0); got != tc.want {
				t.Fatalf("recovered %s after the grab was added, want %s", got, tc.want)
			}
		})
	}
}

// The manager-native outcome windows come from [action_timing]: a shorter
// no_grab_after reports the missing replacement grab sooner, and a shorter
// outcome_window ends tracking.
func TestConfiguredOutcomeWindows(t *testing.T) {
	w := newWorldWith(t, "automatic", 20, "\n[action_timing]\nno_grab_after = \"2h\"\noutcome_window = \"24h\"\n")
	w.stalled(hashA, 1, 1)
	w.run(130 * time.Minute)
	acts, _ := w.svc.Actions(context.Background(), 10)
	if len(acts) != 1 {
		t.Fatalf("actions %+v", acts)
	}
	state := func() string {
		oc, _ := w.st.Outcomes(context.Background(), []int64{acts[0].ID})
		return oc[acts[0].ID].State + ": " + oc[acts[0].ID].Note
	}
	w.run(110 * time.Minute)
	if s := state(); !strings.HasPrefix(s, store.OutcomeAwaitingGrab) {
		t.Fatalf("before no_grab_after: %s", s)
	}
	w.run(20 * time.Minute)
	if s := state(); !strings.HasPrefix(s, store.OutcomeNoGrabYet) || !strings.Contains(s, "in 2h") {
		t.Fatalf("after no_grab_after: %s", s)
	}
	w.run(22 * time.Hour)
	if s := state(); !strings.HasPrefix(s, store.OutcomeUntracked) {
		t.Fatalf("after outcome_window: %s", s)
	}
}
