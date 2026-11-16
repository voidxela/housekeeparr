//go:build integration

package integration

// The complete no-progress recovery workflow on disposable released
// instances, through Housekeeparr's own planner, coordinator, executor, and
// adapters: a managed grab is failed in the manager (blocklisting only that
// release, keeping the torrent), the torrent is stopped while retaining its
// data, and the manager owns the replacement search. Every outcome is then
// verified independently of the executor's own observations.
//
// The stall diagnosis itself is supplied: waiting out the 90-minute
// qualifying window is not useful here, and the diagnosis policy has its
// own deterministic tests. Everything else in the proposal comes from live
// reads.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

type recoveryManager interface {
	workflow.Manager
	Kind() adapter.Kind
	Blocklisted(ctx context.Context, m arr.BlocklistMatch) (bool, error)
}

func ensureSeries(t *testing.T, a arrAPI) int {
	t.Helper()
	var all []struct {
		ID     int `json:"id"`
		TvdbID int `json:"tvdbId"`
	}
	a.mustJSON(http.MethodGet, "/api/v3/series", nil, nil, &all)
	for _, s := range all {
		if s.TvdbID == 999001 {
			return s.ID
		}
	}
	var found []map[string]any
	a.mustJSON(http.MethodGet, "/api/v3/series/lookup", url.Values{"term": {"tvdb:999001"}}, nil, &found)
	if len(found) != 1 {
		t.Fatalf("lookup returned %d series", len(found))
	}
	s := found[0]
	s["qualityProfileId"], s["rootFolderPath"], s["monitored"], s["seasonFolder"] = firstQualityProfile(a), env(t, "HK_IT_WORK")+"/library/tv", true, true
	s["addOptions"] = map[string]any{"monitor": "all", "searchForMissingEpisodes": false, "searchForCutoffUnmetEpisodes": false}
	var out struct {
		ID int `json:"id"`
	}
	a.mustJSON(http.MethodPost, "/api/v3/series", nil, s, &out)
	return out.ID
}

func episodeID(t *testing.T, a arrAPI, seriesID, season, episode int) int {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		var eps []struct {
			ID            int `json:"id"`
			SeasonNumber  int `json:"seasonNumber"`
			EpisodeNumber int `json:"episodeNumber"`
		}
		a.mustJSON(http.MethodGet, "/api/v3/episode", url.Values{"seriesId": {fmt.Sprint(seriesID)}}, nil, &eps)
		for _, e := range eps {
			if e.SeasonNumber == season && e.EpisodeNumber == episode {
				return e.ID
			}
		}
		// The stand-in serves more episodes than an earlier add saw.
		a.commandBody(map[string]any{"name": "RefreshSeries", "seriesId": seriesID})
	}
	t.Fatalf("episode S%02dE%02d never appeared", season, episode)
	return 0
}

func addMovie(t *testing.T, a arrAPI, tmdb int) int {
	t.Helper()
	var found []map[string]any
	a.mustJSON(http.MethodGet, "/api/v3/movie/lookup", url.Values{"term": {fmt.Sprintf("tmdb:%d", tmdb)}}, nil, &found)
	if len(found) != 1 {
		t.Fatalf("lookup returned %d movies", len(found))
	}
	mv := found[0]
	mv["qualityProfileId"], mv["rootFolderPath"], mv["monitored"], mv["minimumAvailability"] = firstQualityProfile(a), env(t, "HK_IT_WORK")+"/library/movies", true, "released"
	mv["addOptions"] = map[string]any{"monitor": "movieOnly", "searchForMovie": false}
	var out struct {
		ID int `json:"id"`
	}
	a.mustJSON(http.MethodPost, "/api/v3/movie", nil, mv, &out)
	return out.ID
}

func TestSonarrRecoveryWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	ep := episodeID(t, a, sid, 1, 5)
	e := mm["tv-e05"]
	waitGrab(t, a, e, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}}, url.Values{"episodeId": {fmt.Sprint(ep)}})
	settleIncomplete(t, e)
	runRecovery(t, a, sonarrClient(t), e, "EpisodeSearch")
}

func TestRadarrRecoveryWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	e := mm["film-e"]
	id := addMovie(t, a, e.Managed.TmdbID)
	waitGrab(t, a, e, map[string]any{"name": "MoviesSearch", "movieIds": []int{id}}, url.Values{"movieId": {fmt.Sprint(id)}})
	settleIncomplete(t, e)
	rc, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runRecovery(t, a, rc, e, "MoviesSearch")
}

func runRecovery(t *testing.T, a arrAPI, mgr recoveryManager, me managedEntry, searchName string) {
	t.Helper()
	ctx := context.Background()
	e := me.asEntry()
	// Production path: an engine cycle with engine-built adapters writes the
	// transfer, queue row, correlation, and probes to a real store; the
	// proposal is planned from those stored records (as the CLI and web do)
	// and executed with the same adapters.
	dir := t.TempDir()
	section, url_ := "sonarr", env(t, "HK_IT_SONARR_URL")
	keyEnv := "HK_IT_SONARR_KEY"
	if mgr.Kind() == adapter.KindRadarr {
		section, url_, keyEnv = "radarr", env(t, "HK_IT_RADARR_URL"), "HK_IT_RADARR_KEY"
	}
	// The managers' download client is whichever the harness selected
	// (--manager-client); the workflow runs through that client's adapter.
	dcMap, clientBlock := managerTOMLConfig(t)
	clientID, clientKind := map[string]string{"qbittorrent": "qbit", "transmission": "tr", "deluge": "deluge", "rtorrent": "rt"}[managerClientKind()],
		adapter.Kind(managerClientKind())
	cfgSrc := "version = 1\n[polling]\njitter = \"0s\"\n[[" + section + "]]\nid = \"" + mgr.ID() + "\"\nurl = \"" + url_ +
		"\"\napi_key = \"env:" + keyEnv + "\"\n" + dcMap + "\n" + clientBlock
	cfg, _, err := config.Parse([]byte(cfgSrc), filepath.Join(dir, "h.toml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, probs := cfg.ResolvedSecrets(nil)
	for _, pr := range probs {
		if pr.Severity == "error" {
			t.Fatal(pr)
		}
	}
	conns, err := engine.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng, err := engine.New(ctx, engine.Options{Config: cfg, Store: st, Conns: conns, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "once", Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	var stored *store.TransferRecord
	var rows []store.QueueRecord
	for i := 0; i < 3; i++ { // correlation may need consecutive cycles
		for _, c := range eng.Cycle(ctx, nil).Connections {
			if !c.OK {
				t.Fatalf("cycle %s: %s", c.ID, c.Error)
			}
		}
		if stored, err = st.Transfer(ctx, clientID, e.Hash); err != nil {
			t.Fatal(err)
		}
		if rows, err = st.QueueItems(ctx, store.QueueFilter{ClientID: clientID, NativeID: e.Hash}); err != nil {
			t.Fatal(err)
		}
		if stored != nil && len(rows) == 1 && rows[0].MatchStatus == store.MatchMatched {
			break
		}
	}
	if stored == nil || len(rows) != 1 || rows[0].MatchStatus != store.MatchMatched {
		t.Fatalf("engine did not record a correlated transfer: %+v %+v", stored, rows)
	}
	tr, row := stored.Latest, &rows[0].Latest
	var cp, mp *adapter.Probe
	recs, _ := st.Connections(ctx)
	for _, c := range recs {
		switch c.ID {
		case clientID:
			cp = c.Probe
		case mgr.ID():
			mp = c.Probe
		}
	}
	if cp == nil || mp == nil {
		t.Fatal("engine stored no probes")
	}
	// Only the diagnosis is supplied: waiting out the stall window is not useful here.
	stored.Assessment = &policy.Assessment{Status: policy.StatusQualified, Diagnosis: policy.DiagnosisNoProgress, Headline: "supplied by the integration test"}
	now := time.Now().UTC()
	in := action.PlanInput{
		Finding:  domain.Finding{ID: 1, Type: domain.FindingTransferStalled, State: domain.FindingOpen},
		Transfer: stored, Queue: rows,
		Policy: policy.Effective{Mode: "automatic", Revision: "it", Automation: []policy.WorkflowPermission{{Workflow: policy.WorkflowNoProgressRecovery, Enabled: true, From: "integration test"}}},
		Origin: action.OriginAutomatic, ClientKind: clientKind, ClientProbe: cp, ManagerKind: mgr.Kind(), ManagerProbe: mp,
		PollInterval: 10 * time.Minute, Now: now,
		// What this test establishes, for exactly these versions.
		Tested: []action.TestedWorkflow{{Workflow: policy.WorkflowNoProgressRecovery, Manager: mgr.Kind(), ManagerVersion: strings.TrimPrefix(mp.Version, "v"),
			Client: clientKind, ClientVersion: strings.TrimPrefix(cp.Version, "v")}},
	}
	p, np := action.Plan(in)
	if np != nil {
		t.Fatal(np.Reason)
	}
	if priv, known := tr.Private.Get(); !known {
		// qBittorrent 4.x does not report privacy: recovery must refuse.
		b := p.Blockers()
		if len(b) != 1 || b[0].Name != "public transfer" {
			t.Fatalf("unknown privacy: blockers %+v", b)
		}
		t.Logf("privacy unknown on qBittorrent %s: recovery correctly refused (%s)", cp.Version, b[0].Detail)
		return
	} else if priv {
		t.Fatal("synthetic release unexpectedly private")
	}
	if !p.Eligible() {
		t.Fatalf("proposal not eligible: %+v", p.Blockers())
	}
	ex := workflow.FromConnections(conns.Clients, conns.Managers)
	co := &action.Coordinator{Store: st, Executor: ex, Actor: "integration"}
	// A stale fingerprint between stored state and the live re-fetch would
	// refuse here with ErrStale rather than send anything.
	digest := payloadDigest(t, e)
	hist0 := a.history(e.Hash)
	start := time.Now().UTC().Add(-time.Second)
	res, err := co.Execute(ctx, *p)
	intent, _ := st.Intent(ctx, res.IntentID)
	b, _ := json.MarshalIndent(intent.Steps, "", " ")
	t.Logf("result %+v err=%v\nsteps %s", res, err, b)
	if err != nil || res.State != store.IntentSucceeded {
		t.Fatalf("workflow did not succeed: %+v %v", res, err)
	}

	// Independent verification, separately for each outcome.
	statuses := []string{a.command("RefreshMonitoredDownloads")}
	time.Sleep(3 * time.Second)
	statuses = append(statuses, a.command("RefreshMonitoredDownloads"))
	time.Sleep(3 * time.Second)
	if a.row(nil, e.Hash) != nil {
		t.Errorf("queue row returned after refreshes %v", statuses)
	}
	blocked, err := mgr.Blocklisted(ctx, arr.BlocklistMatch{SourceTitle: row.Title, Since: start.Add(-time.Minute),
		SeriesID: row.SeriesID, EpisodeID: row.EpisodeID, MovieID: row.MovieID})
	if err != nil || !blocked {
		t.Errorf("release not blocklisted: %v %v", blocked, err)
	}
	hist := a.history(e.Hash)
	if count(hist, "downloadFailed") != count(hist0, "downloadFailed")+1 || count(hist, "grabbed") != count(hist0, "grabbed") {
		t.Errorf("history %v → %v", hist0, hist)
	}
	after, ok := clientState(t, e.Hash)
	if !ok || after.StateClass != domain.StatePaused || after.Category != tr.Category {
		t.Errorf("client after: present=%v %s/%s category %v (was %v)", ok, after.NativeState, after.StateClass, after.Category, tr.Category)
	}
	if d := payloadDigest(t, e); d != digest {
		t.Errorf("payload changed: %s → %s", digest, d)
	}
	searches := a.searchesSince(start)
	found := false
	for _, s := range searches {
		found = found || s.Name == searchName
	}
	if !found {
		t.Errorf("manager queued no replacement %s (skipRedownload=false): %+v", searchName, searches)
	}
	// The local journal holds no lock and records the cooldown.
	if h, _ := st.LockHolder(ctx, p.Locks[0]); h != 0 {
		t.Error("lock still held after success")
	}
	if _, _, ok, _ := st.Cooldown(ctx, p.Locks[0]); !ok {
		t.Error("no cooldown recorded")
	}
	t.Logf("verified: row absent after refreshes, blocklisted, history %v, torrent %s and payload intact, replacement searches %+v",
		hist, after.NativeState, searches)
}
