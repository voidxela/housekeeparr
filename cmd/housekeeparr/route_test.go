package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/control"
	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// While a worker owns the state store, CLI changes are carried out by it
// over the control socket, attributed to the CLI; with no worker answering
// they are refused clearly and nothing is sent.
func TestCLIChangesGoThroughTheRunningWorker(t *testing.T) {
	dir, err := os.MkdirTemp("", "hkr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	d, err := demo.Generate(context.Background(), dir, time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Store.Close() // the "worker" keeps the writer lock
	fs, _ := d.Store.Findings(context.Background(), store.FindingFilter{States: []domain.FindingState{domain.FindingOpen}})
	id := strconv.FormatInt(fs[0].ID, 10)
	cfg := filepath.Join(dir, "housekeeparr.toml")
	if err := os.WriteFile(cfg, []byte(demo.Config), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runCLI(t, "ack", id, "--config", cfg); code != exitStateBusy || !strings.Contains(errOut, "nothing was sent") {
		t.Fatalf("without a worker answering: %d %s", code, errOut)
	}
	svc := app.New(d.Store, d.Config, d.Clock.Now, d.Engine)
	srv, err := control.Listen(svc, d.Config.State.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	code, out, errOut := runCLI(t, "ack", id, "--config", cfg)
	if code != exitOK || !strings.Contains(out, "by the running worker") {
		t.Fatalf("ack through the worker: %d %s %s", code, out, errOut)
	}
	if f, _ := d.Store.Finding(context.Background(), fs[0].ID); f.State != domain.FindingAcknowledged {
		t.Fatalf("finding %s", f.State)
	}
	if code, _, errOut := runCLI(t, "pause", "no_progress_recovery", "--note", "maintenance", "--config", cfg); code != exitOK {
		t.Fatalf("pause through the worker: %d %s", code, errOut)
	}
	if p, _ := d.Store.WorkflowPauses(context.Background()); p["no_progress_recovery"].Actor != string(app.ActorCLI) {
		t.Fatalf("pauses %+v", p)
	}
	if code, _, errOut := runCLI(t, "act", id, "--confirm", "--config", cfg); code != exitProblem || !strings.Contains(errOut, "nothing was sent") {
		t.Fatalf("act refused by the worker: %d %s", code, errOut)
	}
}
