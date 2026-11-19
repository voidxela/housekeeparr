//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// TestDaemonRecovery runs the production worker loop (`housekeeparr run`,
// not `--once`) at the accepted default timing against real instances:
// 10-minute polling with its default jitter, 30-minute new-download grace,
// the 90-minute no-progress window, three observations, one action per
// cycle and the default correlated-stall thresholds. No diagnosis setting is
// overridden; no-progress recovery is enabled globally, as an operator
// would. It takes about two and a half hours and is registered only by
// `scripts/it/run.sh --daemon`.
//
// Every other downloading transfer on the managers' client is stopped
// first, so the two fresh grabs are the client's only active downloads (a
// healthy client with two dead releases). Without that, every harness
// fixture stalls together in the loopback-only namespace, which is exactly
// the shared cause the correlated-stall hold exists for.
//
// Timeline: the worker starts in automatic mode; about 50 minutes in it is
// stopped (SIGTERM) and restarted in paused mode, which must not restart
// qualification; both grabs must qualify while paused with nothing sent;
// the worker is then restarted in automatic mode and must recover one grab
// per cycle. Time to recovery is logged from each grab's client added time.
func TestDaemonRecovery(t *testing.T) {
	ctx := context.Background()
	mm := managedManifest(t)
	work := env(t, "HK_IT_WORK")
	son := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	rad := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	rc, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	type target struct {
		e      managedEntry
		a      arrAPI
		mgr    recoveryManager
		search string
	}
	tv, film := mm["tv-e06"], mm["film-f"]
	if tv.Hash == "" || film.Hash == "" {
		t.Fatal("managed releases tv-e06 and film-f missing from the manifest")
	}
	targets := []target{{tv, son, sonarrClient(t), "EpisodeSearch"}, {film, rad, rc, "MoviesSearch"}}
	isTarget := func(h string) bool { return strings.EqualFold(h, tv.Hash) || strings.EqualFold(h, film.Hash) }

	// Stop every other active download on the managers' client.
	cl := managerClient(t)
	stopper, ok := cl.(interface {
		Stop(context.Context, string) error
	})
	if !ok {
		t.Fatalf("%s adapter cannot stop transfers", managerClientKind())
	}
	snap, err := cl.ListTransfers(ctx)
	if err != nil || !snap.Complete {
		t.Fatalf("listing: %v", err)
	}
	for _, tr := range snap.Transfers {
		if (tr.StateClass == domain.StateDownloading || tr.StateClass == domain.StateMetadata) && !isTarget(tr.NativeID) {
			if err := stopper.Stop(ctx, tr.NativeID); err != nil {
				t.Fatalf("stop %s: %v", tr.Name, err)
			}
			t.Logf("stopped unrelated fixture %s (%s)", tr.Name, tr.NativeState)
		}
	}

	prepareManager(t, son, work+"/library/tv", []int{5000, 5040})
	ep := episodeID(t, son, ensureSeries(t, son), 1, 6)
	waitGrab(t, son, tv, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}}, url.Values{"episodeId": {fmt.Sprint(ep)}})
	settleIncomplete(t, tv)
	prepareManager(t, rad, work+"/library/movies", []int{2000, 2040})
	mid := addMovie(t, rad, film.Managed.TmdbID)
	waitGrab(t, rad, film, map[string]any{"name": "MoviesSearch", "movieIds": []int{mid}}, url.Values{"movieId": {fmt.Sprint(mid)}})
	settleIncomplete(t, film)
	added := map[string]time.Time{}
	for _, tg := range targets {
		tr, ok := clientState(t, tg.e.Hash)
		if !ok {
			t.Fatalf("%s not in the client", tg.e.Key)
		}
		at, known := tr.AddedAt.Get()
		if !known {
			at = time.Now()
		}
		added[tg.e.Hash] = at
	}
	snap, _ = cl.ListTransfers(ctx)
	for _, tr := range snap.Transfers {
		if (tr.StateClass == domain.StateDownloading || tr.StateClass == domain.StateMetadata) && !isTarget(tr.NativeID) {
			t.Fatalf("unexpected active download %s (%s): the targets must be the only ones", tr.Name, tr.NativeState)
		}
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "housekeeparr")
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "../../cmd/housekeeparr")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	clientID := map[string]string{"qbittorrent": "qbit", "transmission": "tr", "deluge": "deluge", "rtorrent": "rt"}[managerClientKind()]
	dcMap, clientBlock := managerTOMLConfig(t)
	stateDir := filepath.Join(dir, "state")
	cfgPath := filepath.Join(dir, "housekeeparr.toml")
	writeConfig := func(mode string) {
		var b strings.Builder
		// Defaults throughout: no [polling], [policy] or [automation.limits].
		fmt.Fprintf(&b, "version = 1\nmode = %q\n[state]\ndir = %q\n[web]\nenabled = false\n[automation.workflows]\nno_progress_recovery = true\n", mode, stateDir)
		fmt.Fprintf(&b, "[[sonarr]]\nid = \"sonarr\"\nurl = %q\napi_key = \"env:HK_IT_SONARR_KEY\"\n%s\n", env(t, "HK_IT_SONARR_URL"), dcMap)
		fmt.Fprintf(&b, "[[radarr]]\nid = \"radarr\"\nurl = %q\napi_key = \"env:HK_IT_RADARR_KEY\"\n%s\n", env(t, "HK_IT_RADARR_URL"), dcMap)
		b.WriteString(clientBlock)
		if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(work, "daemon-worker.log")
	var worker *exec.Cmd
	start := func(mode string) {
		t.Helper()
		writeConfig(mode)
		lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(lf, "=== %s: start worker in %s mode\n", time.Now().UTC().Format(time.RFC3339), mode)
		worker = exec.Command(bin, "run", "--headless", "--config", cfgPath)
		worker.Stdout, worker.Stderr = lf, lf
		if err := worker.Start(); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: worker started in %s mode (pid %d)", time.Now().UTC().Format(time.RFC3339), mode, worker.Process.Pid)
	}
	stop := func() {
		t.Helper()
		if worker == nil {
			return
		}
		_ = worker.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- worker.Wait() }()
		select {
		case err := <-done:
			t.Logf("%s: worker stopped: %v", time.Now().UTC().Format(time.RFC3339), err)
		case <-time.After(60 * time.Second):
			_ = worker.Process.Kill()
			<-done
			t.Errorf("worker did not stop within 60 s of SIGTERM")
		}
		worker = nil
	}
	t.Cleanup(func() {
		stop()
		if b, err := os.ReadFile(logPath); err == nil {
			t.Logf("worker log (tail):\n%s", lastLines(string(b), 60))
		}
	})

	type state struct {
		assess  map[string]*policy.Assessment
		finding map[string]domain.Finding
		eval    map[string]*store.AutomationEvaluation
		intents []store.Intent
		cycles  int
		rows    map[string]domain.QueueItem
		cat     map[string]domain.Signal[string]
	}
	read := func() state {
		t.Helper()
		// The worker creates the database during its first cycle, which
		// starts after up to the default 30 s jitter.
		var st *store.Store
		for deadline := time.Now().Add(3 * time.Minute); ; {
			if st, err = store.Open(ctx, stateDir, store.Options{ReadOnly: true}); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Second)
		}
		defer st.Close()
		s := state{assess: map[string]*policy.Assessment{}, finding: map[string]domain.Finding{}, eval: map[string]*store.AutomationEvaluation{},
			rows: map[string]domain.QueueItem{}, cat: map[string]domain.Signal[string]{}}
		for _, tg := range targets {
			tr, err := st.Transfer(ctx, clientID, tg.e.Hash)
			if err != nil {
				t.Fatal(err)
			}
			if tr != nil {
				s.assess[tg.e.Hash] = tr.Assessment
				s.cat[tg.e.Hash] = tr.Latest.Category
			}
			fs, err := st.Findings(ctx, store.FindingFilter{Type: domain.FindingTransferStalled, ClientID: clientID, NativeID: tg.e.Hash})
			if err != nil {
				t.Fatal(err)
			}
			if len(fs) > 0 {
				s.finding[tg.e.Hash] = fs[0]
				if s.eval[tg.e.Hash], err = st.Evaluation(ctx, fs[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := st.QueueItems(ctx, store.QueueFilter{ClientID: clientID, NativeID: tg.e.Hash})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 1 && rows[0].Present {
				s.rows[tg.e.Hash] = rows[0].Latest
			}
		}
		if s.intents, err = st.Intents(ctx, store.IntentFilter{}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	describe := func(s state) string {
		var b strings.Builder
		for _, tg := range targets {
			a := s.assess[tg.e.Hash]
			if a == nil {
				fmt.Fprintf(&b, " %s: no assessment;", tg.e.Key)
				continue
			}
			fmt.Fprintf(&b, " %s: %s obs=%d elapsed=%s resets=%d", tg.e.Key, a.Status, a.Observations, a.Elapsed, len(a.Resets))
			if ev := s.eval[tg.e.Hash]; ev != nil {
				fmt.Fprintf(&b, " eval=%s", ev.Result)
			}
			b.WriteString(";")
		}
		fmt.Fprintf(&b, " intents=%d", len(s.intents))
		return b.String()
	}
	// wait polls the state every minute until cond holds or the deadline.
	wait := func(label string, d time.Duration, cond func(state) bool) state {
		t.Helper()
		deadline := time.Now().Add(d)
		last := ""
		for {
			s := read()
			if line := describe(s); line != last {
				t.Logf("%s %s:%s", time.Now().UTC().Format("15:04:05"), label, line)
				last = line
			}
			if cond(s) {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: not reached within %s:%s", label, d, describe(s))
			}
			if worker != nil && worker.ProcessState != nil {
				t.Fatalf("worker exited")
			}
			time.Sleep(time.Minute)
		}
	}
	hist0 := map[string][]string{}
	digest := map[string]string{}
	for _, tg := range targets {
		hist0[tg.e.Hash] = tg.a.history(tg.e.Hash)
		digest[tg.e.Hash] = payloadDigest(t, tg.e.asEntry())
	}
	began := time.Now()

	// Phase 1: automatic mode, until about 50 minutes after the grabs.
	start("automatic")
	s := wait("phase 1", 75*time.Minute, func(s state) bool {
		return time.Since(began) >= 50*time.Minute
	})
	if len(s.intents) != 0 {
		t.Fatalf("an action was journaled %s after the grabs, before the default window could elapse", time.Since(began).Round(time.Minute))
	}
	for _, tg := range targets {
		if a := s.assess[tg.e.Hash]; a == nil || a.Status == policy.StatusQualified {
			t.Fatalf("%s: assessment after 50 min %+v, want an unqualified streak", tg.e.Key, a)
		}
	}

	// Restart in paused mode: qualification must continue across it.
	stop()
	time.Sleep(2 * time.Minute)
	start("paused")
	s = wait("paused", 150*time.Minute, func(s state) bool {
		for _, tg := range targets {
			if a := s.assess[tg.e.Hash]; a == nil || a.Status != policy.StatusQualified {
				return false
			}
		}
		return true
	})
	if len(s.intents) != 0 {
		t.Fatalf("paused mode journaled %d actions", len(s.intents))
	}
	for _, tg := range targets {
		a := s.assess[tg.e.Hash]
		q := s.finding[tg.e.Hash].FirstSeen
		t.Logf("%s: qualified (finding #%d opened %s, %s after the client added it); grace ended %s from %s; streak from %s; %d observations, %s counted; resets %+v",
			tg.e.Key, s.finding[tg.e.Hash].ID, q.UTC().Format(time.RFC3339), q.Sub(added[tg.e.Hash]).Round(time.Second),
			a.GraceEnds.UTC().Format(time.RFC3339), a.GraceFrom, a.StreakStart.UTC().Format(time.RFC3339), a.Observations, a.Elapsed, a.Resets)
		if len(a.Resets) != 0 {
			t.Errorf("%s: the restart reset qualification: %+v", tg.e.Key, a.Resets)
		}
		if d := q.Sub(added[tg.e.Hash]); d < 120*time.Minute {
			t.Errorf("%s: qualified %s after it was added, before grace plus window (120 min)", tg.e.Key, d)
		}
		if ev := s.eval[tg.e.Hash]; ev != nil {
			t.Errorf("%s: evaluated for automation while paused: %+v", tg.e.Key, ev)
		}
		if h := tg.a.history(tg.e.Hash); count(h, "downloadFailed") != count(hist0[tg.e.Hash], "downloadFailed") {
			t.Fatalf("%s: manager history changed while paused: %v → %v", tg.e.Key, hist0[tg.e.Hash], h)
		}
	}
	rows, cats := s.rows, s.cat
	for _, tg := range targets {
		if _, ok := rows[tg.e.Hash]; !ok {
			t.Fatalf("%s: no queue row recorded before dispatch", tg.e.Key)
		}
	}

	// Resume automatic mode: one recovery per cycle.
	stop()
	resumed := time.Now().UTC().Add(-time.Second)
	start("automatic")
	s = wait("resumed", 35*time.Minute, func(s state) bool { return len(s.intents) >= 2 })
	for _, in := range s.intents {
		if in.Origin != "automatic" || in.State != store.IntentSucceeded {
			t.Fatalf("intent %d: origin %s state %s (%s)", in.ID, in.Origin, in.State, in.Explanation)
		}
	}
	if gap := s.intents[1].CreatedAt.Sub(s.intents[0].CreatedAt); gap < 5*time.Minute {
		t.Errorf("both recoveries within %s; actions_per_cycle 1 should space them a cycle apart", gap)
	}
	for _, in := range s.intents {
		t.Logf("action #%d journaled %s (%s after resuming): %s", in.ID, in.CreatedAt.UTC().Format(time.RFC3339), in.CreatedAt.Sub(resumed).Round(time.Second), in.Explanation)
	}
	for _, tg := range targets {
		ev := s.eval[tg.e.Hash]
		if ev == nil || ev.Result != store.EvalDispatched {
			t.Fatalf("%s: evaluation %+v, want dispatched", tg.e.Key, ev)
		}
		var at time.Time
		for _, in := range s.intents {
			if in.ID == ev.IntentID {
				at = in.CreatedAt
			}
		}
		t.Logf("%s: recovered %s after the client added it (worker paused for part of that)", tg.e.Key, at.Sub(added[tg.e.Hash]).Round(time.Second))
	}
	stop()

	for _, tg := range targets {
		want := domain.StatePaused
		if tr, ok := clientState(t, tg.e.Hash); ok && !tr.Private.IsKnown() {
			want = domain.StateDownloading
		}
		after, hist, searches, _ := verifyRecovered(t, tg.a, tg.mgr, tg.e.asEntry(), rows[tg.e.Hash], cats[tg.e.Hash], digest[tg.e.Hash], hist0[tg.e.Hash], resumed, tg.search, want)
		t.Logf("%s: verified: history %v, torrent %s, replacement searches %+v", tg.e.Key, hist, after.NativeState, searches)
	}
}
