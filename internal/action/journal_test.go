package action

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/store"
)

var errKilled = errors.New("disk I/O error")

// Every journal failure is reported, never swallowed: the result keeps the
// last durable intent state, the error says whether a request may have
// been sent, and a restart on the same database settles the intent by
// observation without sending anything again.
func TestJournalFailuresAreReportedAndReconciledWithoutResend(t *testing.T) {
	cases := map[string]struct {
		arm func(c *Coordinator, f *fakeExec, dir string)
		// want from Execute
		sent    bool
		durable store.IntentState
		stage   string
		// requests sent in total (before and after restart)
		managerSent, clientSent int
		// after reconciliation
		final    store.IntentState
		released bool
	}{
		"reservation": {
			arm:   func(c *Coordinator, _ *fakeExec, _ string) { c.Store.InjectWriteFault(errKilled) },
			stage: "reservation",
		},
		"high-water marker": {
			arm: func(_ *Coordinator, _ *fakeExec, dir string) {
				if err := os.Mkdir(filepath.Join(dir, "journal.hwm.tmp"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			stage: "high-water", durable: store.IntentCancelled, final: store.IntentCancelled, released: true,
		},
		"before the first dispatch": {
			arm: func(c *Coordinator, f *fakeExec, _ string) {
				f.onBaseline = func(string) { c.Store.InjectWriteFault(errKilled) }
			},
			stage: "dispatching", durable: store.IntentReserved, final: store.IntentCancelled, released: true,
		},
		"after upstream acceptance": {
			arm: func(c *Coordinator, f *fakeExec, _ string) {
				f.onDispatch = func(string) { c.Store.InjectWriteFault(errKilled) }
			},
			sent: true, stage: "outcome of step 1", durable: store.IntentExecuting, managerSent: 1,
			final: store.IntentUncertain,
		},
		"before the second dispatch": {
			arm: func(c *Coordinator, f *fakeExec, _ string) {
				f.onDispatch = func(op string) {
					if op == "manager.queue.fail" {
						c.Store.InjectWriteFaultAfter(1, errKilled)
					}
				}
			},
			sent: true, stage: "step 2 as dispatching", durable: store.IntentExecuting, managerSent: 1,
			final: store.IntentUncertain,
		},
		"terminal commit after the last step": {
			arm: func(c *Coordinator, f *fakeExec, _ string) {
				f.onDispatch = func(op string) {
					if op == "client.stop" {
						c.Store.InjectWriteFaultAfter(1, errKilled)
					}
				}
			},
			sent: true, stage: "recording completion", durable: store.IntentExecuting, managerSent: 1, clientSent: 1,
			final: store.IntentSucceeded,
		},
		"terminal commit after an uncertain step": {
			arm: func(c *Coordinator, f *fakeExec, _ string) {
				f.dispatch["manager.queue.fail"] = func() (json.RawMessage, error) {
					return nil, &adapter.Error{Class: adapter.ErrUncertain, Message: "timeout"}
				}
				f.observe["manager.queue.fail"] = Observation{Verdict: Unknown}
				f.onDispatch = func(string) { c.Store.InjectWriteFaultAfter(1, errKilled) }
			},
			sent: true, stage: "uncertain outcome", durable: store.IntentExecuting, managerSent: 1,
			final: store.IntentUncertain,
		},
		"terminal commit after a definite refusal": {
			arm: func(c *Coordinator, f *fakeExec, _ string) {
				f.dispatch["manager.queue.fail"] = func() (json.RawMessage, error) {
					return nil, &adapter.Error{Class: adapter.ErrPermanent, HTTPStatus: 404}
				}
				f.onDispatch = func(string) { c.Store.InjectWriteFaultAfter(1, errKilled) }
			},
			sent: true, stage: "failed outcome", durable: store.IntentExecuting, managerSent: 1,
			final: store.IntentFailed, released: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, f, dir := setup(t)
			p := proposal("qbit|aa")
			p.Budgets = []BudgetClaim{pacingClaim(5)}
			tc.arm(c, f, dir)
			res, err := c.Execute(ctx, p)
			var je *JournalError
			if !errors.Is(err, ErrJournal) || !errors.As(err, &je) {
				t.Fatalf("want a journal error, got %v (%+v)", err, res)
			}
			if je.Sent != tc.sent || MaybeSent(err) != tc.sent || !strings.Contains(err.Error(), tc.stage) {
				t.Fatalf("sent=%v error %q, want sent=%v stage %q", je.Sent, err, tc.sent, tc.stage)
			}
			told := "no request was sent"
			if tc.sent {
				told = "may have taken effect"
			}
			if !strings.Contains(err.Error(), told) {
				t.Fatalf("operator told %q, want %q", err, told)
			}
			if res.State != tc.durable {
				t.Fatalf("result state %q, want last durable %q", res.State, tc.durable)
			}
			c.Store.InjectWriteFault(nil)
			_ = os.Remove(filepath.Join(dir, "journal.hwm.tmp"))
			if res.IntentID != 0 {
				in, err := c.Store.Intent(ctx, res.IntentID)
				if err != nil {
					t.Fatal(err)
				}
				if in.State != tc.durable {
					t.Fatalf("stored intent %s, result said %s", in.State, tc.durable)
				}
			}
			// Restart on the same database: reconciliation observes; it
			// never sends.
			f.onDispatch, f.onBaseline = nil, nil
			c2 := &Coordinator{Store: c.Store, Executor: f, Now: c.Now}
			if _, err := c2.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if f.count("manager.queue.fail") != tc.managerSent || f.count("client.stop") != tc.clientSent {
				t.Fatalf("sent %v, want %d manager and %d client requests", f.sent, tc.managerSent, tc.clientSent)
			}
			all := intents(t, c)
			if res.IntentID == 0 {
				if len(all) != 0 {
					t.Fatalf("intents %+v", all)
				}
				return
			}
			in, _ := c.Store.Intent(ctx, res.IntentID)
			if in.State != tc.final {
				t.Fatalf("after reconciliation %s (%s), want %s", in.State, in.Explanation, tc.final)
			}
			if held, _ := c.Store.LockHolder(ctx, "transfer:qbit|aa"); (held != 0) != (tc.final == store.IntentUncertain) {
				t.Fatalf("lock holder %d with intent %s", held, in.State)
			}
			used := 1
			if tc.released {
				used = 0
			}
			if u, _ := c.Store.BudgetUsage(ctx, pacingClaim(5).Budget(), 1, t0); u.Used != used {
				t.Fatalf("budget used %d, want %d", u.Used, used)
			}
			// A request that may have taken effect is never sent again for
			// the same observed state.
			if !tc.sent || tc.final == store.IntentFailed {
				return
			}
			f.live = func(p Proposal) Live { return matching(p, false) }
			if _, err := c2.Execute(ctx, p); err == nil {
				t.Fatal("the same proposal executed again")
			}
			if f.count("manager.queue.fail") != tc.managerSent {
				t.Fatalf("duplicate request: %v", f.sent)
			}
		})
	}
}

// A stop requested between steps whose record cannot be written reports
// both the stop and the journal failure.
func TestStopWithUnwritableJournalReportsBoth(t *testing.T) {
	c, f, _ := setup(t)
	cctx, cancel := context.WithCancel(ctx)
	f.onDispatch = func(string) {
		cancel()
		c.Store.InjectWriteFaultAfter(1, errKilled)
	}
	res, err := c.Execute(cctx, proposal("qbit|aa"))
	if !errors.Is(err, context.Canceled) || !MaybeSent(err) || res.State != store.IntentExecuting {
		t.Fatalf("result %+v err %v", res, err)
	}
	if f.count("client.stop") != 0 {
		t.Fatalf("sent %v", f.sent)
	}
}
