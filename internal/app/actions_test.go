package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/action/actiontest"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

type okExec struct{ sent []string }

func (e *okExec) Live(_ context.Context, p action.Proposal) (action.Live, error) {
	return actiontest.Matching(p, len(e.sent) > 0), nil
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
	// The preview reports the attempt just made against the content's
	// allowance, and a reset after review starts it afresh while keeping
	// the history.
	content := pv.Proposal.Planned.ManagerID + "|" + pv.Proposal.Planned.Content[0]
	attempts := func() string {
		pv, _ := svc.Preview(ctx, f.ID, action.OriginManual)
		for _, c := range pv.Proposal.Prerequisites {
			if strings.HasPrefix(c.Name, "recovery attempts for "+content) {
				return c.Detail
			}
		}
		t.Fatalf("no attempts prerequisite in %+v", pv.Proposal.Prerequisites)
		return ""
	}
	if d := attempts(); !strings.HasPrefix(d, "1 of 3 recovery attempts used ever") {
		t.Fatalf("attempts detail %q", d)
	}
	if err := svc.ResetAttempts(ctx, content, app.ActorCLI, ""); err == nil {
		t.Fatal("reset without a note")
	}
	if err := svc.ResetAttempts(ctx, content, app.ActorCLI, "checked the manager history"); err != nil {
		t.Fatal(err)
	}
	if d := attempts(); !strings.HasPrefix(d, "0 of 3 recovery attempts used since the reset") {
		t.Fatalf("attempts after reset %q", d)
	}
	if atts, resets, _ := svc.AttemptHistory(ctx, "attempts:", 0); len(atts) != 1 || len(resets) != 1 {
		t.Fatalf("history %+v %+v", atts, resets)
	}
}

func trimV(v string) string {
	if len(v) > 0 && v[0] == 'v' {
		return v[1:]
	}
	return v
}

type unknownExec struct{ sent int }

func (e *unknownExec) Live(_ context.Context, p action.Proposal) (action.Live, error) {
	return actiontest.Matching(p, e.sent > 0), nil
}
func (e *unknownExec) Dispatch(context.Context, store.Step) (json.RawMessage, error) {
	e.sent++
	return nil, nil
}
func (e *unknownExec) Observe(context.Context, store.Step) (action.Observation, error) {
	return action.Observation{Verdict: action.Unknown, Detail: "unreachable"}, nil
}

// An action interrupted after "dispatching" was committed is reconciled
// before any new action: held as uncertain, never re-sent.
func TestActReconcilesInterruptedActionsFirst(t *testing.T) {
	ctx := context.Background()
	d, err := demo.Generate(ctx, t.TempDir(), time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Store.Close()
	now := d.Clock.Now()
	var id int64
	err = d.Store.Write(ctx, func(tx *store.Tx) error {
		if id, err = tx.CreateIntent(store.Intent{Key: "k", Workflow: "no_progress_recovery", Origin: "manual",
			Locks: []string{"transfer:x|y"}, Steps: []store.Step{{Op: "manager.queue.fail"}, {Op: "client.stop"}}}, now); err != nil {
			return err
		}
		if err := tx.SetIntentState(id, store.IntentExecuting, "", now); err != nil {
			return err
		}
		return tx.BeginStep(id, 1, now)
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := app.New(d.Store, d.Config, d.Clock.Now, nil)
	ex := &unknownExec{}
	svc.SetExecutor(ex)
	_, _, _ = svc.Act(ctx, stalledFindings(t, d)[0].ID, app.ActorCLI)
	in, err := d.Store.Intent(ctx, id)
	if err != nil || in.State != store.IntentUncertain || !in.ReviewRequired || in.Steps[1].State != store.StepSkipped || len(in.Locks) != 1 || ex.sent != 0 {
		t.Fatalf("interrupted intent %+v %v; sent %d", in, err, ex.sent)
	}
	// Review now works on it.
	if err := svc.Review(ctx, id, store.IntentFailed, app.ActorCLI, "checked: not applied"); err != nil {
		t.Fatal(err)
	}
}
