package app

import (
	"context"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Ineligible proposals are classified by reason code and expiry, never by
// wording: a byte-room shortfall is at the ceiling, a cooldown defers until
// it ends, the latest expiry is reported when every blocker returns, and a
// reset is offered only when it alone can unblock.
func TestClassifyUsesReasonCodes(t *testing.T) {
	t1 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(3 * time.Hour)
	blk := func(code action.CheckCode, until *time.Time) action.Check {
		return action.Check{Name: string(code) + " check", Detail: "d", Hard: true, Code: code, Until: until}
	}
	other := action.Check{Name: "supported versions", Detail: "untested", Hard: true}
	cases := map[string]struct {
		checks []action.Check
		want   string
		next   *time.Time
	}{
		"room for this transfer":           {[]action.Check{blk(action.CodeCapacity, nil)}, store.EvalAtCeiling, nil},
		"cooldown only":                    {[]action.Check{blk(action.CodeCooldown, &t1)}, store.EvalDeferred, &t1},
		"cooldown and pacing: latest":      {[]action.Check{blk(action.CodeCooldown, &t1), blk(action.CodePacing, &t2)}, store.EvalDeferred, &t2},
		"windowed attempts":                {[]action.Check{blk(action.CodeAttempts, &t1)}, store.EvalDeferred, &t1},
		"reset-only attempts":              {[]action.Check{blk(action.CodeAttempts, nil)}, store.EvalNeedsReview, nil},
		"reset-only attempts and pacing":   {[]action.Check{blk(action.CodeAttempts, nil), blk(action.CodePacing, &t1)}, store.EvalNeedsReview, nil},
		"attempts beside an unrelated one": {[]action.Check{blk(action.CodeAttempts, nil), other}, store.EvalIneligible, nil},
		"ceiling beside an unrelated one":  {[]action.Check{blk(action.CodeCapacity, nil), other}, store.EvalIneligible, nil},
		"ceiling and reset-only attempts":  {[]action.Check{blk(action.CodeCapacity, nil), blk(action.CodeAttempts, nil)}, store.EvalIneligible, nil},
		"policy wins":                      {[]action.Check{blk(action.CodeCooldown, &t1), blk(action.CodePolicy, nil)}, store.EvalNotPermitted, nil},
		"paused":                           {[]action.Check{blk(action.CodePaused, nil)}, store.EvalNotPermitted, nil},
		"held lock":                        {[]action.Check{blk(action.CodeLock, nil)}, store.EvalIneligible, nil},
	}
	s := &Service{}
	for name, tc := range cases {
		p := &action.Proposal{Prerequisites: tc.checks}
		got, detail, next := s.classify(context.Background(), p, t1)
		if got != tc.want || (next == nil) != (tc.next == nil) || (next != nil && !next.Equal(*tc.next)) {
			t.Errorf("%s: %s next %v, want %s next %v", name, got, next, tc.want, tc.next)
		}
		if detail == "" {
			t.Errorf("%s: no detail", name)
		}
	}
}

func TestProbeSpentOnlyByPossiblyEffectiveRecovery(t *testing.T) {
	step := func(op string, st store.StepState) store.Step { return store.Step{Op: op, State: st} }
	cases := map[string]struct {
		in   store.Intent
		want bool
	}{
		"verified recovery":         {store.Intent{Workflow: "no_progress_recovery", Steps: []store.Step{step("manager.queue.fail", store.StepSucceeded), step("client.stop", store.StepSucceeded)}}, true},
		"uncertain manager request": {store.Intent{Workflow: "metadata_recovery", Steps: []store.Step{step("manager.queue.fail", store.StepUncertain), step("client.stop", store.StepSkipped)}}, true},
		"interrupted while sending": {store.Intent{Workflow: "no_progress_recovery", Steps: []store.Step{step("manager.queue.fail", store.StepDispatching)}}, true},
		"definite rejection":        {store.Intent{Workflow: "no_progress_recovery", Steps: []store.Step{step("manager.queue.fail", store.StepFailed), step("client.stop", store.StepSkipped)}}, false},
		"cancelled baseline":        {store.Intent{Workflow: "no_progress_recovery", Steps: []store.Step{step("manager.queue.fail", store.StepSkipped)}}, false},
		"pack clearance":            {store.Intent{Workflow: "partial_pack", Steps: []store.Step{step("manager.queue.fail", store.StepSucceeded)}}, false},
	}
	for name, tc := range cases {
		if got := probeSpent(tc.in); got != tc.want {
			t.Errorf("%s: spent %v, want %v", name, got, tc.want)
		}
	}
}
