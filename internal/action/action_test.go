package action

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/store"
)

var ctx = context.Background()

// fakeExec is a scripted Executor. dispatch and observe are keyed by op.
type fakeExec struct {
	mu          sync.Mutex
	fingerprint string
	dispatch    map[string]func() (json.RawMessage, error)
	observe     map[string]Observation
	observeErr  map[string]error
	sent        []string
	observed    []string
	onDispatch  func(op string)
}

func newFake() *fakeExec {
	return &fakeExec{fingerprint: "fp1", dispatch: map[string]func() (json.RawMessage, error){},
		observe: map[string]Observation{}, observeErr: map[string]error{}}
}

func (f *fakeExec) Refetch(context.Context, Proposal) (string, error) { return f.fingerprint, nil }

func (f *fakeExec) Dispatch(_ context.Context, st store.Step) (json.RawMessage, error) {
	f.mu.Lock()
	f.sent = append(f.sent, st.Op)
	hook := f.onDispatch
	d := f.dispatch[st.Op]
	f.mu.Unlock()
	if hook != nil {
		hook(st.Op)
	}
	if d != nil {
		return d()
	}
	return json.RawMessage(`{"accepted":true}`), nil
}

func (f *fakeExec) Observe(_ context.Context, st store.Step) (Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observed = append(f.observed, st.Op)
	if err := f.observeErr[st.Op]; err != nil {
		return Observation{}, err
	}
	if o, ok := f.observe[st.Op]; ok {
		return o, nil
	}
	return Observation{Verdict: Holds}, nil
}

func (f *fakeExec) count(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sent {
		if s == op {
			n++
		}
	}
	return n
}

type gate []Check

func (g gate) Blockers(Origin) []Check { return g }

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*Coordinator, *fakeExec, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{Now: func() time.Time { return t0 }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := newFake()
	return &Coordinator{Store: st, Executor: f, Now: func() time.Time { return t0 }}, f, dir
}

func proposal(subject string) Proposal {
	return Proposal{
		Workflow: "no_progress_recovery", Origin: OriginManual, Subject: json.RawMessage(`{"transfer":"` + subject + `"}`),
		Locks: []string{"transfer:" + subject}, Fingerprint: "fp1", PolicyRevision: "p", ConfigRevision: "c",
		Operations: []Operation{
			{Op: "manager.queue.fail", Request: json.RawMessage(`{"removeFromClient":false,"blocklist":true,"skipRedownload":true,"changeCategory":false}`)},
			{Op: "client.stop", Request: json.RawMessage(`{}`)},
		},
		Prerequisites: []Check{{Name: "eligible", Satisfied: true}},
		Cooldown:      time.Hour,
		ExpiresAt:     t0.Add(10 * time.Minute),
	}
}

func intents(t *testing.T, c *Coordinator) []store.Intent {
	t.Helper()
	all, err := c.Store.Intents(ctx, store.IntentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestExecuteSucceedsAndReleasesLocks(t *testing.T) {
	c, f, _ := setup(t)
	res, err := c.Execute(ctx, proposal("qbit|aa"))
	if err != nil || res.State != store.IntentSucceeded {
		t.Fatalf("execute: %+v %v", res, err)
	}
	in, _ := c.Store.Intent(ctx, res.IntentID)
	if len(in.Locks) != 0 || in.Steps[0].State != store.StepSucceeded || in.Steps[1].State != store.StepSucceeded || in.Steps[0].Attempts != 1 {
		t.Fatalf("intent %+v", in)
	}
	if until, _, ok, _ := c.Store.Cooldown(ctx, "transfer:qbit|aa"); !ok || !until.Equal(t0.Add(time.Hour)) {
		t.Fatalf("cooldown %v %v", until, ok)
	}
	if f.count("manager.queue.fail") != 1 || f.count("client.stop") != 1 {
		t.Fatalf("sent %v", f.sent)
	}
	// The same proposal for the same observed state is not executed twice.
	_, err = c.Execute(ctx, proposal("qbit|aa"))
	if !errors.Is(err, ErrConflict) || f.count("manager.queue.fail") != 1 {
		t.Fatalf("duplicate execution: %v sent %v", err, f.sent)
	}
}

func TestRefusalsSendNothingAndJournalNothing(t *testing.T) {
	cases := map[string]func(c *Coordinator, f *fakeExec, p *Proposal){
		"unsatisfied prerequisite": func(_ *Coordinator, _ *fakeExec, p *Proposal) {
			p.Prerequisites = append(p.Prerequisites, Check{Name: "tested write capability", Detail: "not tested", Hard: true})
		},
		"paused": func(c *Coordinator, _ *fakeExec, _ *Proposal) {
			c.Gate = gate{{Name: "mode", Detail: "paused", Hard: true}}
		},
		"expired preview": func(_ *Coordinator, _ *fakeExec, p *Proposal) { p.ExpiresAt = t0.Add(-time.Second) },
		"stale state":     func(_ *Coordinator, f *fakeExec, _ *Proposal) { f.fingerprint = "fp2" },
		"unwritable store": func(c *Coordinator, _ *fakeExec, _ *Proposal) {
			c.Store.InjectWriteFault(errors.New("disk full"))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, f, _ := setup(t)
			p := proposal("qbit|aa")
			mutate(c, f, &p)
			if _, err := c.Execute(ctx, p); err == nil {
				t.Fatal("expected refusal")
			}
			c.Store.InjectWriteFault(nil)
			if len(f.sent) != 0 || len(intents(t, c)) != 0 {
				t.Fatalf("sent %v, intents %d", f.sent, len(intents(t, c)))
			}
		})
	}
}

func TestTimeoutAfterAcceptanceIsHeldNotRetried(t *testing.T) {
	c, f, _ := setup(t)
	f.dispatch["manager.queue.fail"] = func() (json.RawMessage, error) {
		return nil, &adapter.Error{Class: adapter.ErrTransient, Op: "sonarr.queue_delete", Message: "timeout awaiting response"}
	}
	f.observe["manager.queue.fail"] = Observation{Verdict: Unknown, Detail: "queue unreadable"}
	res, err := c.Execute(ctx, proposal("qbit|aa"))
	if err != nil || res.State != store.IntentUncertain {
		t.Fatalf("execute: %+v %v", res, err)
	}
	in, _ := c.Store.Intent(ctx, res.IntentID)
	if !in.ReviewRequired || in.Steps[0].State != store.StepUncertain || in.Steps[1].State != store.StepSkipped || len(in.Locks) != 1 {
		t.Fatalf("intent %+v", in)
	}
	// A new proposal for the same transfer (even with fresh state) is
	// blocked by the held lock; nothing is re-sent.
	f.fingerprint = "fp2"
	p := proposal("qbit|aa")
	p.Fingerprint = "fp2"
	if _, err := c.Execute(ctx, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting execution: %v", err)
	}
	// Reconciliation does not re-send either.
	if _, err := c.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("manager.queue.fail") != 1 || f.count("client.stop") != 0 {
		t.Fatalf("re-sent: %v", f.sent)
	}
	// An operator review resolves it and releases the lock.
	if err := c.Review(ctx, res.IntentID, store.IntentSucceeded, "alex", "checked Sonarr: blocklisted"); err != nil {
		t.Fatal(err)
	}
	if h, _ := c.Store.LockHolder(ctx, "transfer:qbit|aa"); h != 0 {
		t.Fatalf("lock still held after review")
	}
}

func TestTimeoutReconciledByObservation(t *testing.T) {
	c, f, _ := setup(t)
	f.dispatch["manager.queue.fail"] = func() (json.RawMessage, error) {
		return nil, &adapter.Error{Class: adapter.ErrUncertain, Op: "sonarr.queue_delete"}
	}
	res, err := c.Execute(ctx, proposal("qbit|aa"))
	if err != nil || res.State != store.IntentSucceeded || f.count("manager.queue.fail") != 1 {
		t.Fatalf("execute: %+v %v sent %v", res, err, f.sent)
	}
}

func TestAcceptedButNotEffectiveFails(t *testing.T) {
	c, f, _ := setup(t)
	f.observe["manager.queue.fail"] = Observation{Verdict: NotHolds, Detail: "row tracked again after refresh"}
	res, err := c.Execute(ctx, proposal("qbit|aa"))
	if err != nil || res.State != store.IntentFailed || f.count("client.stop") != 0 {
		t.Fatalf("execute: %+v %v sent %v", res, err, f.sent)
	}
	if h, _ := c.Store.LockHolder(ctx, "transfer:qbit|aa"); h != 0 {
		t.Fatal("failed intent kept its lock")
	}
}

func TestPartialFailureIsHeldForReview(t *testing.T) {
	c, f, _ := setup(t)
	f.dispatch["client.stop"] = func() (json.RawMessage, error) {
		return nil, &adapter.Error{Class: adapter.ErrUnauthorized, Op: "qbittorrent.stop", HTTPStatus: 403}
	}
	res, err := c.Execute(ctx, proposal("qbit|aa"))
	if err != nil || res.State != store.IntentUncertain {
		t.Fatalf("execute: %+v %v", res, err)
	}
	in, _ := c.Store.Intent(ctx, res.IntentID)
	if in.Steps[0].State != store.StepSucceeded || in.Steps[1].State != store.StepFailed || len(in.Locks) != 1 {
		t.Fatalf("intent %+v", in)
	}
}

func TestDefiniteFailureOnFirstStepFails(t *testing.T) {
	c, f, _ := setup(t)
	f.dispatch["manager.queue.fail"] = func() (json.RawMessage, error) {
		return nil, &adapter.Error{Class: adapter.ErrPermanent, Op: "sonarr.queue_delete", HTTPStatus: 404}
	}
	res, _ := c.Execute(ctx, proposal("qbit|aa"))
	if res.State != store.IntentFailed || len(f.observed) != 0 {
		t.Fatalf("result %+v observed %v", res, f.observed)
	}
	// A malformed 2xx response is not proof of refusal: uncertain.
	c2, f2, _ := setup(t)
	f2.dispatch["manager.queue.fail"] = func() (json.RawMessage, error) {
		return nil, &adapter.Error{Class: adapter.ErrPermanent, Op: "sonarr.queue_delete", HTTPStatus: 200, Message: "malformed"}
	}
	f2.observe["manager.queue.fail"] = Observation{Verdict: Unknown}
	if res, _ := c2.Execute(ctx, proposal("qbit|aa")); res.State != store.IntentUncertain {
		t.Fatalf("malformed 2xx: %+v", res)
	}
}

// A crash after "dispatching" was committed but before the outcome was:
// simulated by failing every store write once the request is sent.
func TestCrashBetweenNetworkAndCommitIsReconciledWithoutResend(t *testing.T) {
	for _, verdict := range []Verdict{Holds, Unknown} {
		t.Run(string(verdict), func(t *testing.T) {
			c, f, _ := setup(t)
			f.onDispatch = func(string) { c.Store.InjectWriteFault(errors.New("process killed")) }
			res, err := c.Execute(ctx, proposal("qbit|aa"))
			if !errors.Is(err, ErrJournal) || res.State != store.IntentExecuting {
				t.Fatalf("execute: %+v %v", res, err)
			}
			// "Restart": a fresh coordinator on the same store.
			c.Store.InjectWriteFault(nil)
			f.onDispatch = nil
			f.observe["manager.queue.fail"] = Observation{Verdict: verdict}
			c2 := &Coordinator{Store: c.Store, Executor: f, Now: c.Now}
			rep, err := c2.Reconcile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			in, _ := c.Store.Intent(ctx, res.IntentID)
			if f.count("manager.queue.fail") != 1 || f.count("client.stop") != 0 {
				t.Fatalf("reconciliation sent requests: %v", f.sent)
			}
			// Interrupted part-way: never continued, always held.
			if in.State != store.IntentUncertain || len(rep.Uncertain) != 1 || in.Steps[1].State != store.StepSkipped {
				t.Fatalf("intent %+v report %+v", in, rep)
			}
			want := store.StepSucceeded
			if verdict != Holds {
				want = store.StepUncertain
			}
			if in.Steps[0].State != want {
				t.Fatalf("step 1 %s, want %s", in.Steps[0].State, want)
			}
		})
	}
}

// A crash after the intent was journaled but before anything was sent.
func TestCrashBeforeDispatchIsCancelledNotReplayed(t *testing.T) {
	c, f, _ := setup(t)
	p := proposal("qbit|aa")
	err := c.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.CreateIntent(store.Intent{Key: p.Key(), Workflow: p.Workflow, Origin: "automatic", Subject: p.Subject,
			Fingerprint: p.Fingerprint, Locks: p.Locks, Steps: []store.Step{{Op: "manager.queue.fail"}}}, t0)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := c.Reconcile(ctx)
	if err != nil || len(rep.Cancelled) != 1 || len(f.sent) != 0 || len(f.observed) != 0 {
		t.Fatalf("reconcile %+v %v sent %v", rep, err, f.sent)
	}
	if h, _ := c.Store.LockHolder(ctx, "transfer:qbit|aa"); h != 0 {
		t.Fatal("cancelled intent kept its lock")
	}
}

func TestStopBetweenStepsHoldsPartialWork(t *testing.T) {
	c, f, _ := setup(t)
	cctx, cancel := context.WithCancel(ctx)
	f.onDispatch = func(op string) {
		if op == "manager.queue.fail" {
			cancel()
		}
	}
	res, _ := c.Execute(cctx, proposal("qbit|aa"))
	if res.State != store.IntentUncertain || f.count("client.stop") != 0 {
		t.Fatalf("result %+v sent %v", res, f.sent)
	}
}

func TestRestoredDatabaseHoldsWrites(t *testing.T) {
	c, f, dir := setup(t)
	if _, err := c.Execute(ctx, proposal("qbit|aa")); err != nil {
		t.Fatal(err)
	}
	// Simulate a restore: the marker records more than the database has.
	if err := os.WriteFile(filepath.Join(dir, "journal.hwm"), []byte(c.Store.InstanceID()+" 99\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := c.Execute(ctx, proposal("qbit|bb"))
	if !errors.Is(err, ErrRestoreHold) || f.count("manager.queue.fail") != 1 {
		t.Fatalf("restored: %v sent %v", err, f.sent)
	}
	rep, err := c.Reconcile(ctx)
	if err != nil || rep.RestoreHold == nil {
		t.Fatalf("reconcile under restore hold: %+v %v", rep, err)
	}
	if err := c.Store.AcknowledgeRestore(ctx, "alex"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute(ctx, proposal("qbit|bb")); err != nil {
		t.Fatalf("after acknowledgement: %v", err)
	}
}

func TestBudgetPersistsAcrossRestart(t *testing.T) {
	c, f, dir := setup(t)
	claim := []BudgetClaim{{Scope: "global:acquisition", Window: 24 * time.Hour, Limit: 1, Units: 1}}
	p := proposal("qbit|aa")
	p.Budgets = claim
	if _, err := c.Execute(ctx, p); err != nil {
		t.Fatal(err)
	}
	c.Store.Close()
	st, err := store.Open(ctx, dir, store.Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c2 := &Coordinator{Store: st, Executor: f, Now: c.Now}
	p2 := proposal("qbit|bb")
	p2.Budgets = claim
	_, err = c2.Execute(ctx, p2)
	var ref *RefusedError
	if !errors.As(err, &ref) || ref.Checks[0].Name != "budget" || f.count("manager.queue.fail") != 1 {
		t.Fatalf("budget after restart: %v sent %v", err, f.sent)
	}
}
