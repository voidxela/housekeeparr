package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/fake"
)

// withCooldown is the same worker restarted with another success_cooldown.
func (w *world) withCooldown(d time.Duration) *app.Service {
	cfg := *w.svc.Config()
	cfg.Automation.Limits.SuccessCooldown = &config.Duration{Duration: d}
	svc := app.New(w.st, &cfg, w.clock.Now, w.eng)
	svc.SetExecutor(w.ex)
	return svc
}

func cooldownBlocker(p *action.Proposal) (string, bool) {
	for _, b := range p.Blockers() {
		if b.Name == "not in cooldown" || strings.Contains(b.Detail, "in cooldown until") {
			return b.Detail, true
		}
	}
	return "", false
}

// A cooldown recorded under 24 h follows the setting in force: 1 h ends it
// an hour after the action, and 0 (disabled) ends it at once, for both a
// new recovery of the same content and a proactive search for it.
func TestSuccessCooldownFollowsTheCurrentSetting(t *testing.T) {
	w := newWorldLimits(t, "automatic", 20, `success_cooldown = "24h"`, missingRule)
	action.Tested = append(action.Tested, action.TestedWorkflow{Workflow: "missing_search", Manager: "sonarr", ManagerVersion: "0.0.0-fake"})
	w.stalled(hashA, 1, 1)
	w.run(4 * time.Hour)
	acts, _ := w.svc.Actions(context.Background(), 10)
	if len(acts) != 1 {
		t.Fatalf("actions %+v", acts)
	}
	done := *acts[0].FinishedAt
	// The same episode is grabbed again and stalls; it is also missing.
	w.stalled(hashB, 2, 1)
	w.mgr.SetMissing(fake.MissingEpisode(101, 3, "Show S01E01", t0.Add(-72*time.Hour)))
	w.observe(4 * time.Hour)
	f := w.finding(hashB)
	type check struct {
		d    time.Duration
		held bool
	}
	at := w.clock.Now()
	for _, c := range []check{{24 * time.Hour, true}, {time.Hour, false}, {0, false}, {9 * time.Hour, true}} {
		svc := w.withCooldown(c.d)
		pv, err := svc.Preview(context.Background(), f.ID, action.OriginAutomatic)
		if err != nil || pv.Proposal == nil {
			t.Fatalf("%s: preview %v", c.d, err)
		}
		sp, err := svc.PreviewSearch(context.Background(), "sonarr", "episode:101", action.OriginAutomatic)
		if err != nil {
			t.Fatalf("%s: search preview %v", c.d, err)
		}
		for what, p := range map[string]*action.Proposal{"recovery": pv.Proposal, "search": sp.Proposal} {
			detail, held := cooldownBlocker(p)
			if held != c.held {
				t.Errorf("%s at %s after the action, success_cooldown %s: held %v (%s)", what, at.Sub(done), c.d, held, detail)
			}
			if held && !strings.Contains(detail, done.Add(c.d).UTC().Format(time.RFC3339)) {
				t.Errorf("%s: cooldown detail %q", what, detail)
			}
		}
	}
}
