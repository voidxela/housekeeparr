package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/store"
)

const hashE = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

// unmanagedStalls adds downloads no manager knows about that never make
// progress (abandoned manual torrents, say).
func (w *world) unmanagedStalls(hashes ...string) {
	ts, _ := w.client.ListTransfers(context.Background())
	all := ts.Transfers
	for _, h := range hashes {
		all = append(all, fake.Transfer(h, "Manual."+h[:4], domain.StateDownloading, 2*gib, 10*gib, t0))
	}
	w.client.SetTransfers(all...)
}

// runProgressing cycles like run while hash makes verified progress every
// cycle.
func (w *world) runProgressing(hash string, d time.Duration) {
	w.t.Helper()
	for end := w.clock.Now().Add(d); w.clock.Now().Before(end); {
		w.client.Update(hash, func(tr *domain.Transfer) {
			v, _ := tr.VerifiedBytes.Get()
			tr.VerifiedBytes = domain.KnownValue(v + 64<<20)
		})
		w.run(10 * time.Minute)
	}
}

// A client that is demonstrably downloading (another transfer makes
// verified progress) refutes a client-wide shared cause, so stalls that sit
// alongside long-standing unmanaged ones are recovered through bounded
// probes instead of waiting indefinitely; at most one probe per interval.
func TestCorrelatedHoldProbesWhenTheClientIsDownloading(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.unmanagedStalls(hashB, hashC)
	ts, _ := w.client.ListTransfers(context.Background())
	w.client.SetTransfers(append(ts.Transfers, fake.Transfer(hashE, "Healthy", domain.StateDownloading, 1*gib, 10*gib, t0))...)
	w.runProgressing(hashE, 4*time.Hour) // B and C are long qualified
	// Two fresh dead grabs: 4 of 5 active downloads stalled, above the
	// default thresholds.
	w.stalled(hashA, 1, 1)
	w.stalled(hashD, 2, 2)
	w.runProgressing(hashE, 3*time.Hour)
	stops := w.client.Stops()
	if len(stops) == 0 {
		d, _ := w.svc.Finding(context.Background(), w.finding(hashA).ID)
		t.Fatalf("independent stalls on a downloading client waited indefinitely: %+v", d.Automation)
	}
	acts, _ := w.svc.Actions(context.Background(), 10)
	for i := 1; i < len(acts); i++ {
		if gap := acts[i-1].CreatedAt.Sub(acts[i].CreatedAt); gap < time.Hour {
			t.Fatalf("probes %s apart, want at most one per hour: %+v", gap, acts)
		}
	}
	w.runProgressing(hashE, 3*time.Hour)
	if s := w.client.Stops(); len(s) != 2 {
		t.Fatalf("stops %v, want both managed stalls recovered", s)
	}
	if r := w.mgr.Removals(); len(r) != 2 {
		t.Fatalf("removals %+v", r)
	}
}

// With nothing downloading on the client, stalls together are a shared
// cause as far as Housekeeparr can tell: recovery stays held, the finding
// says why, and the user is told once a day, because only they can check
// the network, VPN, tracker or client.
func TestCorrelatedHoldWithoutProgressHoldsAndNotifies(t *testing.T) {
	w := newWorldWith(t, "automatic", 20, "\n[[notify]]\nid = \"hook\"\nurl = \"env:HK_TEST_HOOK\"\n")
	w.unmanagedStalls(hashB, hashC)
	w.stalled(hashA, 1, 1)
	w.run(30 * time.Hour)
	if r := w.mgr.Removals(); len(r) != 0 {
		t.Fatalf("recovered during a correlated stall with no progress anywhere: %+v", r)
	}
	d, _ := w.svc.Finding(context.Background(), w.finding(hashA).ID)
	if d.Automation == nil || d.Automation.Result != store.EvalHeld || !strings.Contains(d.Automation.Detail, "stalled together") {
		t.Fatalf("automation %+v", d.Automation)
	}
	ns, _ := w.st.RecentNotifications(context.Background(), 50)
	held := 0
	for _, n := range ns {
		if strings.Contains(n.Title, "held") {
			held++
		}
	}
	if held != 2 { // 30 hours from 08:00 spans two calendar days
		t.Fatalf("hold notifications %d, want one per day: %+v", held, ns)
	}
}
