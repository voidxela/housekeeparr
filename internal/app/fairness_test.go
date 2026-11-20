package app_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/store"
)

func hashN(i int) string { return fmt.Sprintf("%040x", 0xf000+i) }

// pass runs one observation cycle and the worker's write phase.
func (w *world) pass() (fresh map[string]bool, pr struct {
	removals int
	err      error
}) {
	w.t.Helper()
	w.clock.Advance(10 * time.Minute)
	fresh = w.eng.Cycle(context.Background(), nil).Fresh()
	before := w.removals()
	rep := w.svc.DispatchPass(context.Background(), fresh)
	pr.removals, pr.err = w.removals()-before, rep.Err()
	return fresh, pr
}

// Live refusals have their own bound: five candidates refused by their
// live re-read (nothing journaled) do not use the five action slots, so
// five eligible recoveries behind them all run in the same pass. A backlog
// larger than the slots is worked through over successive passes.
func TestLiveRefusalsDoNotConsumeActionSlots(t *testing.T) {
	w := newWorld(t, "automatic", -1)
	for i := 1; i <= 5; i++ {
		w.stalled(hashN(i), int64(i), int64(i))
		w.mgr.Unmonitor(fmt.Sprintf("episode:%d", 100+i))
	}
	w.observe(3 * time.Hour) // the refused ones are diagnosed first
	for i := 6; i <= 17; i++ {
		w.stalled(hashN(i), int64(i), int64(i))
	}
	w.observe(4 * time.Hour)
	_, pr := w.pass()
	if pr.err != nil || pr.removals != 5 {
		t.Fatalf("first pass sent %d removals (err %v), want 5 behind 5 live refusals", pr.removals, pr.err)
	}
	var sent []int
	for i := 0; i < 3; i++ {
		_, pr := w.pass()
		sent = append(sent, pr.removals)
	}
	if sent[0] != 5 || sent[1] != 2 || sent[2] != 0 {
		t.Fatalf("later passes sent %v, want [5 2 0]: the 12-recovery backlog in slots of 5", sent)
	}
	if len(w.mgr.Removals()) != 12 {
		t.Fatalf("removals %d, want 12 (never the unmonitored)", len(w.mgr.Removals()))
	}
}

// A correlated-stall hold is evidence about a client's network, so it is
// evaluated only from a fresh complete listing: an offline client holds
// nothing new and dispatches nothing, a partial listing is no evidence, and
// the hold returns with the next complete listing. The healthy client
// beside it is unaffected.
func TestCorrelatedHoldNeedsAFreshCompleteListing(t *testing.T) {
	w := newWorldLimits(t, "automatic", 20, noProbes, "")
	w.unmanagedStalls(hashB, hashC)
	w.stalled(hashA, 1, 1)
	w.client2.SetTransfers(fake.Transfer(hashD, "Film.2020", domain.StateDownloading, 2*gib, 10*gib, t0))
	w.radarr.SetQueue(fake.Movie(1, hashD, "Film.2020", "Film", 2020))
	reps := w.run(150 * time.Minute)
	if reps[len(reps)-1].Breakers["client:qbit"] == "" {
		t.Fatal("dark client not held")
	}
	if len(w.radarr.Removals()) != 1 {
		t.Fatalf("healthy client: radarr removals %v", w.radarr.Removals())
	}
	for name, set := range map[string]func(){
		"offline": func() { w.client.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"}) },
		"partial": func() { w.client.Partial(true) },
	} {
		set()
		for _, r := range w.run(30 * time.Minute) {
			if why := r.Breakers["client:qbit"]; why != "" {
				t.Fatalf("%s client still reported held: %s", name, why)
			}
		}
		if len(w.mgr.Removals()) != 0 {
			t.Fatalf("%s client dispatched", name)
		}
		w.client.Fail(nil)
		w.client.Partial(false)
		// After an observation gap the stalls requalify, then hold again.
		held := false
		for _, r := range w.run(150 * time.Minute) {
			held = held || r.Breakers["client:qbit"] != ""
		}
		if !held || len(w.mgr.Removals()) != 0 {
			t.Fatalf("after %s: held %v, removals %v", name, held, w.mgr.Removals())
		}
	}
	// A seeder losing contact: while it had peers nothing was held.
	ts, _ := w.client.ListTransfers(context.Background())
	seed := fake.Transfer(hashE, "Seeding", domain.StateComplete, 10*gib, 10*gib, t0)
	seed.ConnectedPeers = domain.KnownValue[int64](2)
	w.client.SetTransfers(append(ts.Transfers, seed)...)
	if r := w.run(10 * time.Minute); r[0].Breakers["client:qbit"] != "" {
		t.Fatalf("held beside a seeder with peers: %s", r[0].Breakers["client:qbit"])
	}
}

// A correlated-stall probe is spent only by a recovery whose manager
// request may have taken effect: a definitely refused request leaves the
// probe for the next cycle, in the same pass accounting as after restart.
func TestDefinitelyRefusedProbeDoesNotSpendTheInterval(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.unmanagedStalls(hashB, hashC)
	w.stalled(hashA, 1, 1)
	w.stalled(hashD, 2, 2)
	w.mgr.FailRemovals(&adapter.Error{Class: adapter.ErrPermanent, Op: "fake.queue_remove", HTTPStatus: 404, Message: "gone"}, false)
	w.mgr.AfterRemove(func() { w.mgr.FailRemovals(nil, false) })
	w.run(150 * time.Minute)
	w.mgr.AfterRemove(nil)
	acts, _ := w.svc.Actions(context.Background(), 10)
	var failed, ok *store.Intent
	for i := range acts {
		switch acts[i].State {
		case store.IntentFailed:
			failed = &acts[i]
		case store.IntentSucceeded:
			ok = &acts[i]
		}
	}
	if failed == nil || ok == nil {
		t.Fatalf("actions %+v: want a refused probe and a later recovery", acts)
	}
	if gap := ok.CreatedAt.Sub(failed.CreatedAt); gap >= time.Hour {
		t.Fatalf("the refused probe spent the interval: next recovery %s later", gap)
	}
	// Restart: the same accounting from the journal.
	w.restart()
	w.stalled(hashE, 5, 5)
	w.run(150 * time.Minute)
	acts, _ = w.svc.Actions(context.Background(), 10)
	var probes []time.Time
	for i := len(acts) - 1; i >= 0; i-- {
		if acts[i].State == store.IntentSucceeded {
			probes = append(probes, acts[i].CreatedAt)
		}
	}
	if len(probes) < 2 {
		t.Fatalf("actions after restart %+v", acts)
	}
	for i := 1; i < len(probes); i++ {
		if probes[i].Sub(probes[i-1]) < time.Hour {
			t.Fatalf("successful probes %s apart, want at least the interval: %v", probes[i].Sub(probes[i-1]), probes)
		}
	}
}

// Twenty items excluded by local checks do not use the live-evaluation
// cap; repeated live refusals of the first twenty rotate behind the rest.
func TestSearchCapAndRotation(t *testing.T) {
	t.Run("locally excluded", func(t *testing.T) {
		w := searchWorld(t, "automatic")
		var items []domain.MissingItem
		for i := 1; i <= 20; i++ {
			it := fake.MissingEpisode(int64(200+i), 3, fmt.Sprintf("Unaired %d", i), t0.Add(time.Duration(i)*time.Hour))
			it.Available = domain.KnownValue(false)
			items = append(items, it)
		}
		items = append(items, fake.MissingEpisode(301, 3, "Aired", t0.Add(-48*time.Hour)))
		w.mgr.SetMissing(items...)
		w.runSearches(10 * time.Minute)
		if s := w.mgr.Searches(); len(s) != 1 || s[0][0] != 301 {
			t.Fatalf("searches %v", s)
		}
	})
	t.Run("repeated live refusals", func(t *testing.T) {
		w := searchWorld(t, "automatic")
		var items []domain.MissingItem
		for i := 1; i <= 20; i++ {
			items = append(items, fake.MissingEpisode(int64(200+i), 3, fmt.Sprintf("Recent %d", i), t0.Add(-time.Duration(i)*time.Minute)))
			w.mgr.Unmonitor(fmt.Sprintf("episode:%d", 200+i))
		}
		items = append(items, fake.MissingEpisode(301, 3, "Older", t0.Add(-48*time.Hour)))
		w.mgr.SetMissing(items...)
		w.runSearches(time.Hour)
		if s := w.mgr.Searches(); len(s) != 1 || s[0][0] != 301 {
			t.Fatalf("item 21 starved behind 20 live refusals: searches %v", s)
		}
		for i := 1; i <= 20; i++ {
			it, _ := w.st.MissingRecord(context.Background(), "sonarr", fmt.Sprintf("episode:%d", 200+i))
			if it.Searches != 0 || !strings.Contains(it.Note, "refused") {
				t.Fatalf("refused item %d: %+v", i, it)
			}
		}
	})
}

// Beyond the headroom, live reads stop at actions_per_cycle + 5 in a pass;
// the refused findings then go behind the others.
func TestLiveReadsAreBoundedPerPass(t *testing.T) {
	w := newWorld(t, "automatic", -1)
	for i := 1; i <= 12; i++ {
		w.stalled(hashN(i), int64(i), int64(i))
		w.mgr.Unmonitor(fmt.Sprintf("episode:%d", 100+i))
	}
	w.observe(3 * time.Hour)
	for i := 13; i <= 15; i++ {
		w.stalled(hashN(i), int64(i), int64(i))
	}
	w.observe(4 * time.Hour)
	w.clock.Advance(10 * time.Minute)
	fresh := w.eng.Cycle(context.Background(), nil).Fresh()
	rep := w.svc.DispatchPass(context.Background(), fresh)
	refused, waiting := 0, 0
	for _, e := range rep.Recovery.Evaluations {
		switch e.Result {
		case store.EvalRefused:
			refused++
		case store.EvalWaiting:
			waiting++
		}
	}
	if refused != 10 || waiting != 5 || w.removals() != 0 {
		t.Fatalf("refused %d (want the 10-read bound), waiting %d, removals %d", refused, waiting, w.removals())
	}
	if _, pr := w.pass(); pr.removals != 3 {
		t.Fatalf("next pass sent %d, want the 3 eligible ahead of the refused", pr.removals)
	}
}
