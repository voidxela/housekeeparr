package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

var errDisk = errors.New("disk I/O error")

// journalWorld has two recoveries due on different managers and clients,
// and a missing episode a proactive search would look for, all found by
// observation only (no write phase yet).
func journalWorld(t *testing.T) *world {
	w := searchWorld(t, "automatic")
	w.stalled(hashA, 1, 1)
	w.client2.SetTransfers(fake.Transfer(hashB, "Film.2020", domain.StateDownloading, 2*gib, 10*gib, t0))
	w.radarr.SetQueue(fake.Movie(1, hashB, "Film.2020", "Film", 2020))
	w.mgr.SetMissing(fake.MissingEpisode(102, 3, "Show S01E02", t0.Add(-48*time.Hour)))
	w.observe(4 * time.Hour)
	return w
}

// observe cycles without a write phase.
func (w *world) observe(d time.Duration) map[string]bool {
	w.t.Helper()
	var fresh map[string]bool
	for end := w.clock.Now().Add(d); w.clock.Now().Before(end); {
		w.clock.Advance(10 * time.Minute)
		rep := w.eng.Cycle(context.Background(), nil)
		if rep.StoreError != "" {
			w.t.Fatal(rep.StoreError)
		}
		fresh = rep.Fresh()
	}
	return fresh
}

func (w *world) removals() int { return len(w.mgr.Removals()) + len(w.radarr.Removals()) }

// The worker's write phase after a journal failure: the coordinator
// reports it, recovery dispatch stops (the second eligible recovery is not
// contacted), no proactive search starts, and the operator is told whether
// a request may have taken effect. A restart on the same database settles
// the sent request by observation, never re-sends it, and unrelated work
// then proceeds.
func TestJournalFailureAfterSendStopsTheWriteAndIsReconciled(t *testing.T) {
	// A transient fault (only the outcome record fails) is the case where
	// continuing would send more; a persistent one fails everything after.
	for name, times := range map[string]int{"transient": 1, "persistent": 0} {
		t.Run(name, func(t *testing.T) { journalFailureAfterSend(t, times) })
	}
}

func journalFailureAfterSend(t *testing.T, times int) {
	w := journalWorld(t)
	failAfterRemove := func() { w.st.InjectWriteFaults(0, times, errDisk) }
	w.mgr.AfterRemove(failAfterRemove)
	w.radarr.AfterRemove(failAfterRemove)
	w.clock.Advance(10 * time.Minute)
	fresh := w.eng.Cycle(context.Background(), nil).Fresh()
	pr := w.svc.DispatchPass(context.Background(), fresh)

	if w.removals() != 1 || len(w.client.Stops())+len(w.client2.Stops()) != 0 {
		t.Fatalf("removals sonarr %v radarr %v: want exactly one request before the failure", w.mgr.Removals(), w.radarr.Removals())
	}
	if pr.Search != nil || len(w.mgr.Searches()) != 0 || pr.SearchSkipped == "" {
		t.Fatalf("a proactive search followed a journal failure: %+v searches %v", pr, w.mgr.Searches())
	}
	err := pr.Err()
	if !errors.Is(err, action.ErrJournal) || !action.MaybeSent(err) || !strings.Contains(err.Error(), "may have taken effect") {
		t.Fatalf("operator told %v", err)
	}
	evs := pr.Recovery.Evaluations
	if len(evs) != 1 || evs[0].Result != store.EvalDispatched || evs[0].IntentID == 0 || !strings.Contains(evs[0].Detail, "never re-sent") {
		t.Fatalf("evaluations %+v", evs)
	}
	id := evs[0].IntentID
	w.st.InjectWriteFault(nil)
	in, _ := w.st.Intent(context.Background(), id)
	if in.State != store.IntentExecuting || in.Steps[0].State != store.StepDispatching || in.Steps[1].State != store.StepPending {
		t.Fatalf("durable intent %s steps %s/%s", in.State, in.Steps[0].State, in.Steps[1].State)
	}
	if len(in.Locks) == 0 {
		t.Fatal("an unsettled intent released its locks")
	}

	// Restart against the same database.
	w.mgr.AfterRemove(nil)
	w.radarr.AfterRemove(nil)
	w.restart()
	w.run(2 * time.Hour)
	in, _ = w.st.Intent(context.Background(), id)
	if in.State != store.IntentUncertain || in.Steps[0].State != store.StepSucceeded || in.Steps[1].State != store.StepSkipped {
		t.Fatalf("after reconciliation %s (%s)", in.State, in.Explanation)
	}
	// The first manager's request was not repeated; the other recovery,
	// outside the held scopes, then ran once.
	if len(w.mgr.Removals()) != 1 || len(w.radarr.Removals()) != 1 {
		t.Fatalf("after restart: sonarr %v radarr %v", w.mgr.Removals(), w.radarr.Removals())
	}
}

// A journal failure before any request: nothing was sent, the operator is
// told so, and after a restart the intent is cancelled and the recovery
// runs exactly once.
func TestJournalFailureBeforeSendIsReportedAsUnsent(t *testing.T) {
	w := journalWorld(t)
	rec := w.executor()
	fe := &faultExec{Recovery: rec, before: func() { w.st.InjectWriteFault(errDisk) }}
	w.svc.SetExecutor(fe)
	w.clock.Advance(10 * time.Minute)
	fresh := w.eng.Cycle(context.Background(), nil).Fresh()
	pr := w.svc.DispatchPass(context.Background(), fresh)
	err := pr.Err()
	if w.removals() != 0 || len(w.mgr.Searches()) != 0 || pr.Search != nil {
		t.Fatalf("sent: removals %d searches %v", w.removals(), w.mgr.Searches())
	}
	if !errors.Is(err, action.ErrJournal) || action.MaybeSent(err) || !strings.Contains(err.Error(), "no request was sent") {
		t.Fatalf("operator told %v", err)
	}
	if evs := pr.Recovery.Evaluations; len(evs) != 1 || evs[0].Result != store.EvalRefused || !strings.HasPrefix(evs[0].Detail, "nothing was sent") {
		t.Fatalf("evaluations %+v", evs)
	}
	w.st.InjectWriteFault(nil)
	w.restart()
	w.svc.SetExecutor(rec)
	w.run(2 * time.Hour)
	if len(w.mgr.Removals()) != 1 || len(w.radarr.Removals()) != 1 {
		t.Fatalf("after restart: sonarr %v radarr %v", w.mgr.Removals(), w.radarr.Removals())
	}
	acts, _ := w.svc.Actions(context.Background(), 10)
	var cancelled, succeeded int
	for _, a := range acts {
		switch a.State {
		case store.IntentCancelled:
			cancelled++
		case store.IntentSucceeded:
			succeeded++
		}
	}
	if cancelled != 1 || succeeded != 2 {
		t.Fatalf("actions %+v", acts)
	}
}

// An ordinary scoped refusal is not a journal failure: unrelated work in
// the same pass continues, including the proactive search.
func TestScopedRefusalLeavesUnrelatedWork(t *testing.T) {
	w := journalWorld(t)
	w.mgr.Unmonitor("episode:101")
	w.clock.Advance(10 * time.Minute)
	fresh := w.eng.Cycle(context.Background(), nil).Fresh()
	pr := w.svc.DispatchPass(context.Background(), fresh)
	if pr.Err() != nil {
		t.Fatal(pr.Err())
	}
	if len(w.mgr.Removals()) != 0 || len(w.radarr.Removals()) != 1 {
		t.Fatalf("sonarr %v radarr %v", w.mgr.Removals(), w.radarr.Removals())
	}
	if pr.Search == nil || pr.SearchSkipped != "" {
		t.Fatalf("search pass did not run: %+v", pr)
	}
}

// faultExec runs before() immediately before the first step's baseline is
// read, so the journal fails after planning and before any request.
type faultExec struct {
	*workflow.Recovery
	before func()
	done   bool
}

func (f *faultExec) Baseline(ctx context.Context, st store.Step) (json.RawMessage, error) {
	if !f.done {
		f.done = true
		f.before()
	}
	return f.Recovery.Baseline(ctx, st)
}
