//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// TestAutomaticRecovery drives the production automatic path end to end on
// real instances: the released `housekeeparr run --once` binary, repeated
// on one state directory, observes a fresh Sonarr grab and a fresh Radarr
// grab that genuinely make no progress (no peer can ever connect), the real
// engine diagnoses both from its own observations, and after each cycle
// DispatchAutomatic plans against the compiled action.Tested registry and
// executes through the shared coordinator. Nothing is supplied or injected.
//
// Two transfer-scoped [[override]]s shorten the diagnosis windows (grace
// 0s, window 1m, 3 observations) and enable no-progress automation for
// exactly these two transfers; global automation stays off, so no other
// transfer on the client can be dispatched, and the other never-progressing
// fixtures keep the default 90-minute window (they neither qualify nor trip
// the correlated-stall circuit breaker). The default windows are covered by
// deterministic policy tests only.
func TestAutomaticRecovery(t *testing.T) {
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

	// Fresh grabs, each settled as an incomplete download right away (on
	// Transmission, before the manager can treat it as complete).
	prepareManager(t, son, work+"/library/tv", []int{5000, 5040})
	ep := episodeID(t, son, ensureSeries(t, son), 1, 6)
	waitGrab(t, son, tv, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}}, url.Values{"episodeId": {fmt.Sprint(ep)}})
	settleIncomplete(t, tv)
	prepareManager(t, rad, work+"/library/movies", []int{2000, 2040})
	mid := addMovie(t, rad, film.Managed.TmdbID)
	waitGrab(t, rad, film, map[string]any{"name": "MoviesSearch", "movieIds": []int{mid}}, url.Values{"movieId": {fmt.Sprint(mid)}})
	settleIncomplete(t, film)

	// The production binary, built static as released.
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
		fmt.Fprintf(&b, "version = 1\nmode = %q\n[state]\ndir = %q\n[web]\nenabled = false\n[polling]\ninterval = \"1m\"\njitter = \"0s\"\n", mode, stateDir)
		fmt.Fprintf(&b, "[[sonarr]]\nid = \"sonarr\"\nurl = %q\napi_key = \"env:HK_IT_SONARR_KEY\"\n%s\n", env(t, "HK_IT_SONARR_URL"), dcMap)
		fmt.Fprintf(&b, "[[radarr]]\nid = \"radarr\"\nurl = %q\napi_key = \"env:HK_IT_RADARR_KEY\"\n%s\n", env(t, "HK_IT_RADARR_URL"), dcMap)
		b.WriteString(clientBlock)
		for _, tg := range targets {
			fmt.Fprintf(&b, "\n[[override]]\ntransfer = %q\nreason = \"integration test: short diagnosis window for this synthetic stall\"\n"+
				"[override.policy]\nnew_download_grace = \"0s\"\nno_progress_window = \"1m\"\nmin_observations = 3\n"+
				"[override.automation]\nno_progress_recovery = true\n", clientID+"|"+tg.e.Hash)
		}
		if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runOnce := func(label string) {
		t.Helper()
		cmd := exec.Command(bin, "run", "--once", "--headless", "--config", cfgPath)
		out, err := cmd.CombinedOutput()
		t.Logf("%s: run --once: err=%v\n%s", label, err, lastLines(string(out), 12))
		if err != nil {
			t.Fatalf("%s: run --once failed: %v", label, err)
		}
	}
	type snapshot struct {
		findings  map[string]domain.Finding // by hash: latest no-progress finding, any state
		qualified map[string]bool
		intents   []store.Intent
		evals     map[string]*store.AutomationEvaluation
		probes    map[string]*adapter.Probe
		rows      map[string]domain.QueueItem
		category  map[string]domain.Signal[string]
	}
	read := func() snapshot {
		t.Helper()
		st, err := store.Open(ctx, stateDir, store.Options{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		s := snapshot{findings: map[string]domain.Finding{}, qualified: map[string]bool{}, evals: map[string]*store.AutomationEvaluation{},
			probes: map[string]*adapter.Probe{}, rows: map[string]domain.QueueItem{}, category: map[string]domain.Signal[string]{}}
		fs, err := st.Findings(ctx, store.FindingFilter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			for _, tg := range targets {
				if f.Type == domain.FindingTransferStalled && f.Subject.ClientID == clientID && f.Subject.NativeID == tg.e.Hash && f.ID > s.findings[tg.e.Hash].ID {
					s.findings[tg.e.Hash] = f
					ev, err := st.Evaluation(ctx, f.ID)
					if err != nil {
						t.Fatal(err)
					}
					s.evals[tg.e.Hash] = ev
				}
			}
		}
		for _, tg := range targets {
			tr, err := st.Transfer(ctx, clientID, tg.e.Hash)
			if err != nil {
				t.Fatal(err)
			}
			if tr != nil {
				s.qualified[tg.e.Hash] = tr.Assessment != nil && tr.Assessment.Status == policy.StatusQualified
				s.category[tg.e.Hash] = tr.Latest.Category
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
		cs, err := st.Connections(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			s.probes[c.ID] = c.Probe
		}
		return s
	}

	// Phase 1, observe mode: the engine must diagnose both stalls from its
	// own observations, and nothing may be sent.
	writeConfig("observe")
	hist0 := map[string][]string{}
	for _, tg := range targets {
		hist0[tg.e.Hash] = tg.a.history(tg.e.Hash)
	}
	var s snapshot
	deadline := time.Now().Add(5 * time.Minute)
	for i := 1; ; i++ {
		runOnce(fmt.Sprintf("observe cycle %d", i))
		s = read()
		if len(s.findings) == 2 && s.qualified[tv.Hash] && s.qualified[film.Hash] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine did not diagnose both stalls in time: findings %v qualified %v", s.findings, s.qualified)
		}
		time.Sleep(20 * time.Second)
	}
	if len(s.intents) != 0 {
		t.Fatalf("observe mode journaled %d actions", len(s.intents))
	}
	for _, tg := range targets {
		if h := tg.a.history(tg.e.Hash); count(h, "downloadFailed") != count(hist0[tg.e.Hash], "downloadFailed") {
			t.Fatalf("%s: manager history changed in observe mode: %v → %v", tg.e.Key, hist0[tg.e.Hash], h)
		}
		if tr, ok := clientState(t, tg.e.Hash); !ok || tr.StateClass != domain.StateDownloading {
			t.Fatalf("%s: client state changed in observe mode: %v %s", tg.e.Key, ok, tr.StateClass)
		}
		t.Logf("%s: diagnosed by the engine: finding #%d %q", tg.e.Key, s.findings[tg.e.Hash].ID, s.findings[tg.e.Hash].Title)
	}

	// Is this exact combination registered? The binary plans against the
	// compiled registry; mirror it to know which outcome to require.
	cp := s.probes[clientID]
	if cp == nil {
		t.Fatal("no client probe stored")
	}
	registered := func(m adapter.Kind, mid string) bool {
		mp := s.probes[mid]
		for _, tw := range action.Tested {
			if mp != nil && tw.Workflow == policy.WorkflowNoProgressRecovery && tw.Manager == m && tw.ManagerVersion == strings.TrimPrefix(mp.Version, "v") &&
				tw.Client == adapter.Kind(managerClientKind()) && tw.ClientVersion == strings.TrimPrefix(cp.Version, "v") {
				return true
			}
		}
		return false
	}
	eligible := registered(adapter.KindSonarr, "sonarr") && registered(adapter.KindRadarr, "radarr")

	// Phase 2, automatic mode.
	writeConfig("automatic")
	digest := map[string]string{}
	for _, tg := range targets {
		digest[tg.e.Hash] = payloadDigest(t, tg.e.asEntry())
		hist0[tg.e.Hash] = tg.a.history(tg.e.Hash)
	}
	rows, cats := s.rows, s.category
	start := time.Now().UTC().Add(-time.Second)

	if !eligible {
		// Unregistered (or privacy-unknown) combination: every pass refuses
		// before any request, visibly.
		for i := 1; i <= 2; i++ {
			runOnce(fmt.Sprintf("automatic pass %d (unregistered %s %s)", i, managerClientKind(), cp.Version))
		}
		s = read()
		if len(s.intents) != 0 {
			t.Fatalf("an unregistered combination journaled %d actions", len(s.intents))
		}
		for _, tg := range targets {
			ev := s.evals[tg.e.Hash]
			if ev == nil || ev.Result == store.EvalDispatched {
				t.Fatalf("%s: evaluation %+v, want a visible refusal", tg.e.Key, ev)
			}
			if h := tg.a.history(tg.e.Hash); count(h, "downloadFailed") != count(hist0[tg.e.Hash], "downloadFailed") {
				t.Fatalf("%s: manager history changed: %v → %v", tg.e.Key, hist0[tg.e.Hash], h)
			}
			t.Logf("%s on %s %s: automatic recovery correctly refused: %s: %s", tg.e.Key, managerClientKind(), cp.Version, ev.Result, ev.Detail)
		}
		return
	}

	// The first pass dispatches exactly one action (actions_per_cycle is
	// 1); the other eligible finding waits for the next cycle.
	runOnce("automatic pass 1")
	s = read()
	if len(s.intents) != 1 {
		t.Fatalf("pass 1 journaled %d actions, want exactly 1", len(s.intents))
	}
	first := s.intents[0]
	var firstHash, otherHash string
	for _, tg := range targets {
		ev := s.evals[tg.e.Hash]
		if ev == nil {
			t.Fatalf("%s: no automatic evaluation after pass 1", tg.e.Key)
		}
		t.Logf("pass 1: %s: %s %s", tg.e.Key, ev.Result, ev.Detail)
		switch ev.Result {
		case store.EvalDispatched:
			firstHash = tg.e.Hash
			if ev.IntentID != first.ID {
				t.Fatalf("%s: dispatched intent %d, journal has %d", tg.e.Key, ev.IntentID, first.ID)
			}
		case store.EvalWaiting:
			otherHash = tg.e.Hash
		default:
			t.Fatalf("%s: pass 1 result %s (%s), want one dispatched and one waiting", tg.e.Key, ev.Result, ev.Detail)
		}
	}
	if firstHash == "" || otherHash == "" {
		t.Fatal("pass 1 did not dispatch one finding and defer the other")
	}
	if first.Origin != string(action.OriginAutomatic) || first.State != store.IntentSucceeded {
		t.Fatalf("intent %d: origin %s state %s (%s)", first.ID, first.Origin, first.State, first.Explanation)
	}

	runOnce("automatic pass 2")
	s = read()
	if len(s.intents) != 2 {
		t.Fatalf("after pass 2: %d actions, want 2", len(s.intents))
	}
	if ev := s.evals[otherHash]; ev == nil || ev.Result != store.EvalDispatched {
		t.Fatalf("pass 2: waiting finding not dispatched: %+v", ev)
	}
	for _, in := range s.intents {
		if in.Origin != string(action.OriginAutomatic) || in.State != store.IntentSucceeded {
			t.Fatalf("intent %d: origin %s state %s (%s)", in.ID, in.Origin, in.State, in.Explanation)
		}
	}

	// Later cycles send nothing more for either finding.
	for i := 3; i <= 4; i++ {
		runOnce(fmt.Sprintf("automatic pass %d", i))
	}
	if s = read(); len(s.intents) != 2 {
		t.Fatalf("later passes journaled more actions: %d", len(s.intents))
	}

	// The operator view shows the automatic decision.
	f := s.findings[firstHash]
	out, err := exec.Command(bin, "finding", fmt.Sprint(f.ID), "--json", "--config", cfgPath).CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"result": "dispatched"`) {
		t.Errorf("finding %d --json does not show the dispatched decision: %v\n%s", f.ID, err, lastLines(string(out), 30))
	}

	// Independent verification on the real instances, for each target.
	for _, tg := range targets {
		row, ok := rows[tg.e.Hash]
		if !ok {
			t.Fatalf("%s: no queue row recorded before dispatch", tg.e.Key)
		}
		after, hist, searches := verifyRecovered(t, tg.a, tg.mgr, tg.e.asEntry(), row, cats[tg.e.Hash], digest[tg.e.Hash], hist0[tg.e.Hash], start, tg.search)
		t.Logf("%s: verified after automatic dispatch: history %v, torrent %s, replacement searches %+v", tg.e.Key, hist, after.NativeState, searches)
	}
	// The binary recorded each manager's replacement search as its own
	// outcome.
	st, err := store.Open(ctx, stateDir, store.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, in := range s.intents {
		var subj struct {
			NativeID string `json:"native_id"`
		}
		_ = json.Unmarshal(in.Subject, &subj)
		name := "EpisodeSearch"
		if strings.EqualFold(subj.NativeID, film.Hash) {
			name = "MoviesSearch"
		}
		requireSearchOutcome(t, st, in.ID, name)
	}
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}
