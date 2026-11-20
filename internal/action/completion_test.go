package action

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/store"
)

type finished struct {
	state    store.IntentState
	cooldown map[string]time.Time
	reason   string
	retained store.RetainedTransfer
}

func finishOf(t *testing.T, c *Coordinator, id int64) finished {
	t.Helper()
	in, err := c.Store.Intent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	out := finished{state: in.State, cooldown: map[string]time.Time{}}
	cs, err := c.Store.ActiveCooldowns(ctx, t0, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range cs {
		out.cooldown[k] = v.Until
		out.reason = v.Reason
	}
	ret, _ := c.Store.RetainedTransfers(ctx, false, 0)
	if len(ret) == 1 {
		out.retained = ret[0]
		out.retained.RetainedAt = time.Time{}
	}
	return out
}

// Completing normally and settling by reconciliation after a crash at
// either commit leave the same durable result: the intent, a cooldown on
// each original lock subject from the planned duration, and the retained
// transfer with its planned size and content.
func TestReconciledCompletionMatchesUninterrupted(t *testing.T) {
	c, _, _ := setup(t)
	res, err := c.Execute(ctx, proposal("qbit|aa"))
	if err != nil {
		t.Fatal(err)
	}
	want := finishOf(t, c, res.IntentID)
	if want.state != store.IntentSucceeded || !want.cooldown["transfer:qbit|aa"].Equal(t0.Add(time.Hour)) ||
		want.retained.WantedBytes == nil || *want.retained.WantedBytes != 100 || want.retained.Content != "episode:1" {
		t.Fatalf("uninterrupted: %+v", want)
	}
	for name, after := range map[string]int{"before the last outcome commit": 0, "after the last step commit": 1} {
		t.Run(name, func(t *testing.T) {
			c, f, _ := setup(t)
			// LegacyCooldown differs from the journaled 1 h: the journaled
			// value must win.
			c.LegacyCooldown = 6 * time.Hour
			f.onDispatch = func(op string) {
				if op == "client.stop" {
					c.Store.InjectWriteFaultAfter(after, errKilled)
				}
			}
			res, err := c.Execute(ctx, proposal("qbit|aa"))
			if !MaybeSent(err) {
				t.Fatalf("execute: %+v %v", res, err)
			}
			c.Store.InjectWriteFault(nil)
			f.onDispatch = nil
			if _, err := c.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			got := finishOf(t, c, res.IntentID)
			if got.state != want.state || len(got.cooldown) != len(want.cooldown) || got.reason != want.reason {
				t.Fatalf("reconciled %+v, uninterrupted %+v", got, want)
			}
			for k, v := range want.cooldown {
				if !got.cooldown[k].Equal(v) {
					t.Fatalf("cooldown %s until %s, uninterrupted %s", k, got.cooldown[k], v)
				}
			}
			if got.retained.WantedBytes == nil || *got.retained.WantedBytes != 100 || got.retained.Content != want.retained.Content ||
				got.retained.Name != want.retained.Name || got.retained.Disposition != want.retained.Disposition {
				t.Fatalf("retained %+v, uninterrupted %+v", got.retained, want.retained)
			}
		})
	}
}

// An intent journaled before cooldowns were recorded with it takes the
// current setting when reconciled, and says so; one whose workflow sets no
// cooldown gets none.
func TestLegacyIntentCooldownOnReconciliation(t *testing.T) {
	for wf, wantCooldown := range map[string]bool{"no_progress_recovery": true, "missing_search": false} {
		t.Run(wf, func(t *testing.T) {
			c, f, _ := setup(t)
			c.LegacyCooldown = 2 * time.Hour
			p := proposal("qbit|aa")
			p.Workflow = wf
			dispatched := t0.Add(-time.Minute)
			err := c.Store.Write(ctx, func(tx *store.Tx) error {
				legacyPlan, _ := json.Marshal(map[string]any{"operations": p.Operations})
				id, err := tx.CreateIntent(store.Intent{Key: p.Key(), Workflow: wf, Origin: "automatic", Subject: p.Subject, Plan: legacyPlan,
					Fingerprint: p.Fingerprint, Locks: p.Locks, Steps: []store.Step{{Op: "client.stop", Target: p.Operations[1].Target}}}, t0)
				if err != nil {
					return err
				}
				if err := tx.SetIntentState(id, store.IntentExecuting, "", dispatched); err != nil {
					return err
				}
				return tx.BeginStep(id, 1, dispatched, nil)
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Reconcile(ctx); err != nil || len(f.sent) != 0 {
				t.Fatalf("reconcile: %v sent %v", err, f.sent)
			}
			cs, _ := c.Store.ActiveCooldowns(ctx, t0, 24*time.Hour)
			cd, ok := cs["transfer:qbit|aa"]
			if ok != wantCooldown {
				t.Fatalf("cooldown %+v, want one: %v", cs, wantCooldown)
			}
			if ok && (!cd.Until.Equal(dispatched.Add(2*time.Hour)) || !strings.Contains(cd.Reason, "predates recorded cooldowns")) {
				t.Fatalf("legacy cooldown %+v", cd)
			}
			// Without journaled sizes, the retained transfer's size stays unknown.
			if ret, _ := c.Store.RetainedTransfers(ctx, false, 0); len(ret) != 1 || ret[0].WantedBytes != nil || ret[0].VerifiedBytes != nil {
				t.Fatalf("retained %+v", ret)
			}
		})
	}
}
