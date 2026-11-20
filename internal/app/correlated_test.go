package app_test

import (
	"context"
	"slices"
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
// verified progress) reaches swarms, so stalls beside long-standing
// unmanaged ones are the releases' own: there is no hold, and each is
// recovered as soon as it qualifies, without probe spacing.
func TestCorrelatedHoldDoesNotApplyWhileTheClientIsDownloading(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.unmanagedStalls(hashB, hashC)
	ts, _ := w.client.ListTransfers(context.Background())
	w.client.SetTransfers(append(ts.Transfers, fake.Transfer(hashE, "Healthy", domain.StateDownloading, 1*gib, 10*gib, t0))...)
	w.runProgressing(hashE, 4*time.Hour) // B and C are long qualified
	// Two fresh dead grabs: 4 of 5 active downloads stalled.
	w.stalled(hashA, 1, 1)
	w.stalled(hashD, 2, 2)
	var held bool
	for end := w.clock.Now().Add(3 * time.Hour); w.clock.Now().Before(end); {
		w.client.Update(hashE, func(tr *domain.Transfer) {
			v, _ := tr.VerifiedBytes.Get()
			tr.VerifiedBytes = domain.KnownValue(v + 64<<20)
		})
		for _, r := range w.run(10 * time.Minute) {
			held = held || r.Breakers["client:qbit"] != ""
		}
	}
	if held {
		t.Fatal("a downloading client was held")
	}
	if s := w.client.Stops(); len(s) != 2 {
		t.Fatalf("stops %v, want both managed stalls recovered", s)
	}
	acts, _ := w.svc.Actions(context.Background(), 10)
	if len(acts) != 2 || acts[0].CreatedAt.Sub(acts[1].CreatedAt) >= time.Hour {
		t.Fatalf("actions %+v: want both recovered together, not spaced as probes", acts)
	}
}

// Production trial, 2026-11-20: every incomplete download on the client was
// a dead release (half of them magnets without metadata, none with peers),
// while seeding torrents there had peers. A seeding transfer's peers prove
// the network, VPN and client work, so nothing is held and every stall is
// recovered.
func TestSeedingPeersShowTheClientWorks(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.metadataStalls(hashB, hashC, hashE)
	ts, _ := w.client.ListTransfers(context.Background())
	seed := fake.Transfer("ffffffffffffffffffffffffffffffffffffffff", "Seeding", domain.StateComplete, 10*gib, 10*gib, t0)
	seed.ConnectedPeers = domain.KnownValue[int64](3)
	w.client.SetTransfers(append(ts.Transfers, seed)...)
	w.stalled(hashA, 1, 1)
	w.stalled(hashD, 2, 2)
	for _, r := range w.run(150 * time.Minute) {
		if r.Breakers["client:qbit"] != "" {
			t.Fatalf("held although a seeding transfer has peers: %s", r.Breakers["client:qbit"])
		}
	}
	if s := w.client.Stops(); !slices.Contains(s, hashA) || !slices.Contains(s, hashD) {
		t.Fatalf("stops %v, want both managed stalls recovered", s)
	}
}

// With no transfer on the client showing contact or progress, stalls
// together point to the client, its network or VPN: recovery is held, the
// finding says why, the user is told once a day, and one recovery passes
// per probe interval so a client of only dead releases still progresses.
func TestDarkClientHoldsWithSpacedProbesAndNotifies(t *testing.T) {
	w := newWorldWith(t, "automatic", 20, "\n[[notify]]\nid = \"hook\"\nurl = \"env:HK_TEST_HOOK\"\n")
	w.unmanagedStalls(hashB, hashC)
	w.stalled(hashA, 1, 1)
	w.stalled(hashD, 2, 2)
	w.run(150 * time.Minute)
	b, _ := w.svc.Finding(context.Background(), w.finding(hashA).ID)
	d, _ := w.svc.Finding(context.Background(), w.finding(hashD).ID)
	probed, waiting := b, d
	if len(w.client.Stops()) == 1 && w.client.Stops()[0] == hashD {
		probed, waiting = d, b
	}
	if s := w.client.Stops(); len(s) != 1 {
		t.Fatalf("stops %v, want exactly one probe within the first interval", s)
	}
	if !strings.Contains(probed.Automation.Detail, "correlated-stall probe") {
		t.Fatalf("probe detail: %s", probed.Automation.Detail)
	}
	if waiting.Automation == nil || waiting.Automation.Result != store.EvalHeld || !strings.Contains(waiting.Automation.Detail, "no transfer there") {
		t.Fatalf("held detail: %+v", waiting.Automation)
	}
	w.run(28 * time.Hour)
	acts, _ := w.svc.Actions(context.Background(), 10)
	if len(acts) != 2 || acts[0].CreatedAt.Sub(acts[1].CreatedAt) < time.Hour {
		t.Fatalf("actions %+v: want both recovered, at least one probe interval apart", acts)
	}
	ns, _ := w.st.RecentNotifications(context.Background(), 50)
	held := 0
	for _, n := range ns {
		if strings.Contains(n.Title, "held") {
			held++
		}
	}
	if held < 1 || held > 2 { // about 30 hours from 08:00 spans two calendar days
		t.Fatalf("hold notifications %d, want at most one per day: %+v", held, ns)
	}
}

// With probes turned off, a dark client's hold stays closed.
func TestDarkClientHoldWithoutProbes(t *testing.T) {
	w := newWorldLimits(t, "automatic", 20, noProbes, "")
	w.unmanagedStalls(hashB, hashC)
	w.stalled(hashA, 1, 1)
	w.run(30 * time.Hour)
	if r := w.mgr.Removals(); len(r) != 0 {
		t.Fatalf("recovered on a dark client with probes off: %+v", r)
	}
}

// metadataStalls adds unmanaged magnets that never obtain metadata.
func (w *world) metadataStalls(hashes ...string) {
	ts, _ := w.client.ListTransfers(context.Background())
	all := ts.Transfers
	for _, h := range hashes {
		all = append(all, fake.Transfer(h, "Magnet."+h[:4], domain.StateMetadata, 0, 0, t0))
	}
	w.client.SetTransfers(all...)
}

// Connected peers on any download, even a stalled one whose peers lack its
// pieces, show the client and its network work: nothing is held.
func TestConnectedPeersLiftTheHold(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.unmanagedStalls(hashB, hashC)
	w.client.Update(hashB, func(tr *domain.Transfer) { tr.ConnectedPeers = domain.KnownValue[int64](2) })
	w.stalled(hashA, 1, 1)
	w.stalled(hashD, 2, 2)
	w.run(150 * time.Minute)
	if s := w.client.Stops(); len(s) != 2 {
		d, _ := w.svc.Finding(context.Background(), w.finding(hashA).ID)
		t.Fatalf("stops %v: %+v", s, d.Automation)
	}
}

// The last pass's holds, with their evidence time and next probe, are
// available to the overview and status between passes.
func TestOverviewShowsLastPassHolds(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.unmanagedStalls(hashB, hashC)
	w.stalled(hashA, 1, 1)
	w.stalled(hashD, 2, 2)
	w.run(150 * time.Minute)
	o, err := w.svc.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if o.Holds == nil || !o.Holds.EvaluatedAt.Equal(w.clock.Now()) {
		t.Fatalf("holds %+v", o.Holds)
	}
	var found bool
	for _, h := range o.Holds.Holds {
		if h.Scope == "client:qbit" && h.Correlated && h.Probing && h.NextProbe != nil && strings.Contains(h.Reason, "stalled together") {
			found = true
		}
	}
	if !found {
		t.Fatalf("correlated hold with next probe missing: %+v", o.Holds.Holds)
	}
}
