package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// A pause made after a preview still stops the action: the gate, evaluated
// before every write, reads the current pauses, for manual and automatic
// origins alike.
func TestGateRefusesAPausedWorkflowBeforeEveryWrite(t *testing.T) {
	ctx := context.Background()
	cfg, _, err := config.Parse([]byte("version = 1\nmode = \"automatic\"\n[automation.workflows]\nno_progress_recovery = true\n"), "/x/h.toml")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, cfg, time.Now, nil)
	pol, err := cfg.Resolve(policy.Subject{})
	if err != nil {
		t.Fatal(err)
	}
	for _, origin := range []action.Origin{action.OriginManual, action.OriginAutomatic} {
		p := action.Proposal{Workflow: string(policy.WorkflowNoProgressRecovery), Origin: origin, PolicyRevision: pol.Revision}
		for _, c := range (gate{svc}).Blockers(ctx, p) {
			t.Fatalf("%s: unexpected blocker before the pause: %+v", origin, c)
		}
		if err := svc.PauseWorkflow(ctx, policy.WorkflowNoProgressRecovery, ActorWeb, "investigating"); err != nil {
			t.Fatal(err)
		}
		bs := (gate{svc}).Blockers(ctx, p)
		if len(bs) != 1 || bs[0].Name != "workflow not paused" || !bs[0].Hard || !strings.Contains(bs[0].Detail, "investigating") {
			t.Fatalf("%s: blockers %+v", origin, bs)
		}
		// Pausing another workflow does not affect this one.
		if err := svc.ResumeWorkflow(ctx, policy.WorkflowNoProgressRecovery, ActorWeb, "ok"); err != nil {
			t.Fatal(err)
		}
		if err := svc.PauseWorkflow(ctx, policy.WorkflowMetadataRecovery, ActorWeb, "other"); err != nil {
			t.Fatal(err)
		}
		if bs := (gate{svc}).Blockers(ctx, p); len(bs) != 0 {
			t.Fatalf("%s: another workflow's pause blocked this one: %+v", origin, bs)
		}
		if err := svc.ResumeWorkflow(ctx, policy.WorkflowMetadataRecovery, ActorWeb, "ok"); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.PauseWorkflow(ctx, "no_such_workflow", ActorWeb, "x"); err == nil {
		t.Fatal("unknown workflow paused")
	}
}
