package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

type okExec struct{ sent []string }

func (e *okExec) Refetch(_ context.Context, p action.Proposal) (string, error) {
	return p.Fingerprint, nil
}
func (e *okExec) Dispatch(_ context.Context, st store.Step) (json.RawMessage, error) {
	e.sent = append(e.sent, st.Op)
	return nil, nil
}
func (e *okExec) Observe(context.Context, store.Step) (action.Observation, error) {
	return action.Observation{Verdict: action.Holds}, nil
}

func stalledFindings(t *testing.T, d *demo.Demo) []domain.Finding {
	t.Helper()
	fs, err := d.Store.Findings(context.Background(), store.FindingFilter{States: []domain.FindingState{domain.FindingOpen}})
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.Finding
	for _, f := range fs {
		if f.Type == domain.FindingTransferStalled {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		t.Fatal("demo has no stalled finding")
	}
	return out
}

func names(cs []action.Check) map[string]bool {
	out := map[string]bool{}
	for _, c := range cs {
		out[c.Name] = true
	}
	return out
}

// TestPreviewAndActShareTheCoordinator drives the shared service used by
// both the CLI and the web interface on engine-produced demo state.
func TestPreviewAndActShareTheCoordinator(t *testing.T) {
	ctx := context.Background()
	end := time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC)
	d, err := demo.Generate(ctx, t.TempDir(), end)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Store.Close()
	svc := app.New(d.Store, d.Config, d.Clock.Now, nil)
	stalled := stalledFindings(t, d)
	f := stalled[0]

	pv, err := svc.Preview(ctx, f.ID, action.OriginManual)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Proposal == nil {
		t.Fatalf("no proposal: %s", pv.NoAction)
	}
	b := names(pv.Proposal.Blockers())
	if !b["tested write workflow"] || !b["manual action permitted by policy"] {
		t.Fatalf("expected the untested-workflow and observe-mode blockers, got %v", b)
	}
	// Refused before anything is journaled or sent.
	_, _, err = svc.Act(ctx, f.ID, app.ActorCLI)
	var ref *action.RefusedError
	if !errors.As(err, &ref) {
		t.Fatalf("act: %v", err)
	}
	if all, _ := svc.Actions(ctx, 10); len(all) != 0 {
		t.Fatalf("refused action journaled: %+v", all)
	}

	// The same path executes once every prerequisite holds: register the
	// demo's versions as tested and leave observe mode. Remaining blockers
	// (if any) come from the demo's own evidence, so the test only goes on
	// when the planner reports none.
	saved := action.Tested
	defer func() { action.Tested = saved }()
	conns, _ := d.Store.Connections(ctx)
	var cv, mv string
	var ck, mk adapter.Kind
	for _, c := range conns {
		if c.Probe == nil {
			continue
		}
		if c.ID == f.Subject.ClientID {
			cv, ck = c.Probe.Version, adapter.Kind(c.Kind)
		}
		if c.Kind == string(adapter.KindSonarr) || c.Kind == string(adapter.KindRadarr) {
			if mv == "" {
				mv, mk = c.Probe.Version, adapter.Kind(c.Kind)
			}
		}
	}
	action.Tested = []action.TestedWorkflow{{Workflow: "no_progress_recovery", Client: ck, ClientVersion: trimV(cv), Manager: mk, ManagerVersion: trimV(mv)}}
	d.Config.Mode = "paused"
	var eligible *domain.Finding
	for i, sf := range stalled {
		pv, err = svc.Preview(ctx, sf.ID, action.OriginManual)
		if err != nil {
			t.Fatal(err)
		}
		if names(pv.Proposal.Blockers())["manual action permitted by policy"] {
			t.Fatalf("paused mode blocks manual actions: %v", pv.Proposal.Blockers())
		}
		if names(pv.Proposal.Blockers())["tested write workflow"] {
			t.Fatalf("registered combination still untested: %v", pv.Proposal.Blockers())
		}
		if pv.Proposal.Eligible() {
			eligible = &stalled[i]
		} else {
			t.Logf("finding %d stays ineligible on demo evidence: %v", sf.ID, pv.Proposal.Blockers())
		}
	}
	if eligible == nil {
		t.Fatal("no demo stalled finding becomes eligible; the executor path would go untested")
	}
	f = *eligible
	// Without an executor the service refuses rather than pretending.
	if _, _, err := svc.Act(ctx, f.ID, app.ActorCLI); !errors.Is(err, app.ErrNoExecutor) {
		t.Fatalf("no executor: %v", err)
	}
	ex := &okExec{}
	svc.SetExecutor(ex)
	_, res, err := svc.Act(ctx, f.ID, app.ActorWeb)
	if err != nil || res.State != store.IntentSucceeded || len(ex.sent) == 0 {
		t.Fatalf("act: %+v %v sent %v", res, err, ex.sent)
	}
	act, _ := svc.Activity(ctx, 10)
	if len(act.Actions) != 1 || act.Actions[0].Origin != "manual" {
		t.Fatalf("activity actions %+v", act.Actions)
	}
	// The transfer is now locked out by cooldown for a repeat.
	pv, _ = svc.Preview(ctx, f.ID, action.OriginManual)
	if !names(pv.Proposal.Blockers())["not in cooldown"] {
		t.Fatalf("cooldown not applied: %v", pv.Proposal.Blockers())
	}
}

func trimV(v string) string {
	if len(v) > 0 && v[0] == 'v' {
		return v[1:]
	}
	return v
}
