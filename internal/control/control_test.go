package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

func demoSvc(t *testing.T) (*app.Service, *demo.Demo, string) {
	t.Helper()
	d, err := demo.Generate(context.Background(), t.TempDir(), time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Store.Close() })
	// A short socket directory: Unix socket paths are limited in length.
	dir, err := os.MkdirTemp("", "hkc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return app.New(d.Store, d.Config, d.Clock.Now, d.Engine), d, dir
}

func openFinding(t *testing.T, svc *app.Service) int64 {
	t.Helper()
	fs, err := svc.Findings(context.Background(), app.FindingQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.State == domain.FindingOpen {
			return f.ID
		}
	}
	t.Fatal("no open finding")
	return 0
}

// Requests run through the worker's service with the CLI actor; the socket
// is owner-only; concurrent requests are serialised and each answered.
func TestRequestsRunThroughTheWorker(t *testing.T) {
	svc, _, dir := demoSvc(t)
	srv, err := Listen(svc, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if fi, err := os.Stat(Path(dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v %v", fi.Mode(), err)
	}
	id := openFinding(t, svc)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make([]Response, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := Send(ctx, dir, Request{Op: "ack", ID: id})
			if err != nil {
				t.Error(err)
			}
			results[i] = r
		}()
	}
	wg.Wait()
	ok := 0
	for _, r := range results {
		if r.OK {
			ok++
		}
	}
	if ok == 0 {
		t.Fatalf("no acknowledgement succeeded: %+v", results)
	}
	d, _ := svc.Finding(ctx, id)
	if d.State != domain.FindingAcknowledged {
		t.Fatalf("finding %s", d.State)
	}
	var byCLI bool
	for _, e := range d.Events {
		byCLI = byCLI || (e.Kind == "acknowledged" && e.Actor == string(app.ActorCLI))
	}
	if !byCLI {
		t.Fatalf("events %+v: want the acknowledgement attributed to the CLI", d.Events)
	}
	// Refusals and unknown targets are reported as such.
	if r, _ := Send(ctx, dir, Request{Op: "act", ID: id}); r.OK || !r.Refused {
		t.Fatalf("unconfirmed act: %+v", r)
	}
	if r, _ := Send(ctx, dir, Request{Op: "act", ID: id, Confirm: true}); r.OK || !r.Refused {
		t.Fatalf("act without an executor: %+v", r)
	}
	if r, _ := Send(ctx, dir, Request{Op: "ack", ID: 99999}); r.OK || !r.NotFound {
		t.Fatalf("unknown finding: %+v", r)
	}
	if r, _ := Send(ctx, dir, Request{Op: "pause", Workflow: "no_progress_recovery"}); r.OK || r.Error == "" {
		t.Fatalf("pause without a note: %+v", r)
	}
}

// An uncertain action is reviewed through the worker.
func TestReviewOfAnUncertainActionThroughTheWorker(t *testing.T) {
	svc, d, dir := demoSvc(t)
	srv, err := Listen(svc, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	now := d.Clock.Now()
	var id int64
	if err := d.Store.Write(context.Background(), func(tx *store.Tx) error {
		var err error
		if id, err = tx.CreateIntent(store.Intent{Key: "ctl-unc", Workflow: "no_progress_recovery", Origin: "automatic", Locks: []string{"transfer:x|y"}}, now); err != nil {
			return err
		}
		if err := tx.SetIntentState(id, store.IntentExecuting, "", now); err != nil {
			return err
		}
		return tx.SetIntentState(id, store.IntentUncertain, "timeout", now)
	}); err != nil {
		t.Fatal(err)
	}
	r, err := Send(context.Background(), dir, Request{Op: "review", ID: id, To: "succeeded", Note: "checked the manager's history"})
	if err != nil || !r.OK {
		t.Fatalf("review: %+v %v", r, err)
	}
	in, _ := d.Store.Intent(context.Background(), id)
	if in.State != store.IntentSucceeded || len(in.Locks) != 0 {
		t.Fatalf("intent %+v", in)
	}
}

// Without a worker nothing is sent; a socket left by a worker that is gone
// is replaced; a live one is not taken over.
func TestStaleAndMissingWorkers(t *testing.T) {
	svc, _, dir := demoSvc(t)
	if _, err := Send(context.Background(), dir, Request{Op: "ack", ID: 1}); !errors.Is(err, ErrNoWorker) || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("no worker: %v", err)
	}
	if err := os.WriteFile(Path(dir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(context.Background(), dir, Request{Op: "ack", ID: 1}); !errors.Is(err, ErrNoWorker) {
		t.Fatalf("stale socket file: %v", err)
	}
	srv, err := Listen(svc, dir)
	if err != nil {
		t.Fatalf("listen over a stale socket: %v", err)
	}
	if _, err := Listen(svc, dir); err == nil {
		t.Fatal("a second worker took over a live socket")
	}
	srv.Close()
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Fatalf("socket left after close: %v", err)
	}
}

// A worker lost after it read the request: the outcome is unknown, and the
// client says so without retrying.
func TestLostAnswerIsUnknownAndNotRetried(t *testing.T) {
	dir, err := os.MkdirTemp("", "hkc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, SocketName))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var mu sync.Mutex
	received := 0
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var r Request
			_ = json.NewDecoder(c).Decode(&r)
			mu.Lock()
			received++
			mu.Unlock()
			c.Close() // the worker stops before answering
		}
	}()
	_, err = Send(context.Background(), dir, Request{Op: "act", ID: 7, Confirm: true})
	if !errors.Is(err, ErrLost) || !strings.Contains(err.Error(), "may or may not have been carried out") {
		t.Fatalf("lost answer: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if received != 1 {
		t.Fatalf("the request was delivered %d times", received)
	}
}
