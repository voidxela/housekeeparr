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
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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

// Metadata recovery: the manager grabs a magnet whose metadata can never
// arrive (random hash, no tracker, loopback-only namespace). Its privacy is
// unknown on every client, so the scope opts metadata_recovery in to
// unknown privacy, as an operator would for a public-only category.
func TestSonarrMetadataRecoveryWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	ep := episodeID(t, a, sid, 1, 7)
	e := mm["tv-e07-magnet"]
	waitGrab(t, a, e, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}}, url.Values{"episodeId": {fmt.Sprint(ep)}})
	settleMetadata(t, e)
	runRecoveryFor(t, a, sonarrClient(t), e, "EpisodeSearch", policy.WorkflowMetadataRecovery)
}

func TestRadarrMetadataRecoveryWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	e := mm["film-g-magnet"]
	id := addMovie(t, a, e.Managed.TmdbID)
	waitGrab(t, a, e, map[string]any{"name": "MoviesSearch", "movieIds": []int{id}}, url.Values{"movieId": {fmt.Sprint(id)}})
	settleMetadata(t, e)
	rc, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runRecoveryFor(t, a, rc, e, "MoviesSearch", policy.WorkflowMetadataRecovery)
}

// Private no-progress recovery under the default private_recovery mode:
// the grab is failed and blocklisted and the manager searches again, while
// the private torrent is never stopped.
func TestSonarrPrivateRecoveryWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	ep := episodeID(t, a, sid, 1, 8)
	e := mm["tv-e08-private"]
	waitGrab(t, a, e, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}}, url.Values{"episodeId": {fmt.Sprint(ep)}})
	settleIncomplete(t, e)
	runRecoveryMode(t, a, sonarrClient(t), e, "EpisodeSearch", policy.WorkflowNoProgressRecovery, policy.PrivateKeepRunning)
}

func TestRadarrPrivateRecoveryWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	e := mm["film-h-private"]
	id := addMovie(t, a, e.Managed.TmdbID)
	waitGrab(t, a, e, map[string]any{"name": "MoviesSearch", "movieIds": []int{id}}, url.Values{"movieId": {fmt.Sprint(id)}})
	settleIncomplete(t, e)
	rc, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runRecoveryMode(t, a, rc, e, "MoviesSearch", policy.WorkflowNoProgressRecovery, policy.PrivateKeepRunning)
}

// Preserved-data failure handling (SPEC §7.2): the grab announces to the
// stand-in's tracker, which rejects every announce as "Unregistered
// torrent". The real adapter must report the rejection, and the workflow
// fails and blocklists the grab and stops the torrent, keeping it and its
// data.
func TestSonarrPreservedDataFailureWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	ep := episodeID(t, a, sid, 1, 9)
	e := mm["tv-e09-unregistered"]
	waitGrab(t, a, e, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}}, url.Values{"episodeId": {fmt.Sprint(ep)}})
	settleIncomplete(t, e)
	if !settleRejected(t, e) {
		return
	}
	runRecoveryFor(t, a, sonarrClient(t), e, "EpisodeSearch", policy.WorkflowPreservedDataFailure)
}

func TestRadarrPreservedDataFailureWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	e := mm["film-i-unregistered"]
	id := addMovie(t, a, e.Managed.TmdbID)
	waitGrab(t, a, e, map[string]any{"name": "MoviesSearch", "movieIds": []int{id}}, url.Values{"movieId": {fmt.Sprint(id)}})
	settleIncomplete(t, e)
	if !settleRejected(t, e) {
		return
	}
	rc, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runRecoveryFor(t, a, rc, e, "MoviesSearch", policy.WorkflowPreservedDataFailure)
}

// A private torrent its tracker rejects follows private_recovery's default
// keep_running: the grab is failed and blocklisted, the torrent never
// stopped (user decision 2026-11-17).
func TestSonarrPrivatePreservedDataFailureWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	ep := episodeID(t, a, sid, 1, 10)
	e := mm["tv-e10-unregistered-private"]
	waitGrab(t, a, e, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}}, url.Values{"episodeId": {fmt.Sprint(ep)}})
	settleIncomplete(t, e)
	if !settleRejected(t, e) {
		return
	}
	runRecoveryMode(t, a, sonarrClient(t), e, "EpisodeSearch", policy.WorkflowPreservedDataFailure, policy.PrivateKeepRunning)
}

func TestRadarrPrivatePreservedDataFailureWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	e := mm["film-j-unregistered-private"]
	id := addMovie(t, a, e.Managed.TmdbID)
	waitGrab(t, a, e, map[string]any{"name": "MoviesSearch", "movieIds": []int{id}}, url.Values{"movieId": {fmt.Sprint(id)}})
	settleIncomplete(t, e)
	if !settleRejected(t, e) {
		return
	}
	rc, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runRecoveryMode(t, a, rc, e, "MoviesSearch", policy.WorkflowPreservedDataFailure, policy.PrivateKeepRunning)
}

// settleRejected waits until the client reports the tracker's rejection
// (every adapter classifies tracker results from a structured field).
func settleRejected(t *testing.T, e managedEntry) bool {
	t.Helper()
	if path := os.Getenv("HK_IT_QBT_SESSION_LOG"); managerClientKind() == "qbittorrent" && path != "" {
		if log, err := os.ReadFile(path); err == nil {
			for _, line := range strings.Split(string(log), "\n") {
				if strings.Contains(line, "BitTorrent session encountered a serious error") && strings.Contains(line, "Operation not permitted") {
					t.Fatal("qBittorrent's BitTorrent session reported a denied socket operation; this host cannot exercise the tracker fixture")
				}
			}
		}
	}
	deadline := time.Now().Add(120 * time.Second)
	var last domain.Transfer
	nudged := false
	for time.Now().Before(deadline) {
		if tr, ok := clientState(t, e.Hash); ok {
			last = tr
			// qBittorrent resets a tracker's state when its check of the
			// existing payload finishes after the first announce, and then
			// waits its retry interval; the harness (not Housekeeparr) asks
			// it to announce again once, rather than waiting minutes.
			if managerClientKind() == "qbittorrent" && !nudged && !tr.SourceFailure.IsKnown() && tr.StateClass == domain.StateDownloading {
				nudged = true
				qbtReannounce(t, e.Hash)
				t.Logf("%s: no tracker answer reported (%s); harness requested a re-announce", e.Key, tr.SourceFailure.Note)
			}
			if v, ok := tr.SourceFailure.Get(); ok && v == domain.SourceUnregistered {
				t.Logf("%s: tracker rejection reported: %s/%s message %q", e.Key, tr.NativeState, tr.StateClass, tr.ClientMessage.Value)
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s: tracker rejection never reported (last %s/%s source %s %q: %q)", e.Key, last.NativeState, last.StateClass, last.SourceFailure.Status,
		last.SourceFailure.Value, last.SourceFailure.Note)
	return false
}

// settleMetadata waits until the client reports the grab as acquiring
// metadata (with privacy and size unknown).
func settleMetadata(t *testing.T, e managedEntry) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last domain.Transfer
	for time.Now().Before(deadline) {
		if tr, ok := clientState(t, e.Hash); ok {
			last = tr
			if tr.StateClass == domain.StateMetadata {
				t.Logf("%s: fetching metadata in client: %s, private %v, wanted %v", e.Key, tr.NativeState, tr.Private, tr.WantedBytes)
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("%s: grab never settled as metadata acquisition (last %s/%s)", e.Key, last.NativeState, last.StateClass)
}

func runRecovery(t *testing.T, a arrAPI, mgr recoveryManager, me managedEntry, searchName string) {
	t.Helper()
	runRecoveryFor(t, a, mgr, me, searchName, policy.WorkflowNoProgressRecovery)
}

func runRecoveryFor(t *testing.T, a arrAPI, mgr recoveryManager, me managedEntry, searchName string, wf policy.Workflow) {
	t.Helper()
	runRecoveryMode(t, a, mgr, me, searchName, wf, "")
}

// runRecoveryMode runs a recovery; a non-empty private mode expects the
// transfer to be private and plans under that private_recovery mode.
func runRecoveryMode(t *testing.T, a arrAPI, mgr recoveryManager, me managedEntry, searchName string, wf policy.Workflow, private policy.PrivateMode) {
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
	diag, ftype := policy.DiagnosisNoProgress, domain.FindingTransferStalled
	switch wf {
	case policy.WorkflowMetadataRecovery:
		diag, ftype = policy.DiagnosisMetadataTimeout, domain.FindingMetadataTimeout
	case policy.WorkflowPreservedDataFailure:
		// The real adapter's stored classification is the evidence here;
		// only the confirmation window is not waited out.
		if v, ok := stored.Latest.SourceFailure.Get(); !ok || v != domain.SourceUnregistered {
			t.Fatalf("engine stored no unregistered rejection: %+v", stored.Latest.SourceFailure)
		}
		diag, ftype = policy.DiagnosisSourceUnregistered, domain.FindingSourceRejected
	}
	// The supplied diagnosis is made under the plan's (default) settings.
	stored.Assessment = &policy.Assessment{Status: policy.StatusQualified, Diagnosis: diag, Headline: "supplied by the integration test",
		PolicyRevision: policy.Settings{}.Revision()}
	pol := policy.Effective{Mode: "automatic", Revision: "it", Automation: []policy.WorkflowPermission{{Workflow: wf, Enabled: true, From: "integration test"}}}
	// As the built-in defaults: unknown privacy is accepted where defined
	// (treated as private for recovery), and private_recovery keeps the
	// torrent running unless the test asks otherwise.
	if policy.UnknownPrivacyDefined(wf) {
		pol.UnknownPrivacy = []policy.WorkflowPermission{{Workflow: wf, Enabled: true, From: "integration test"}}
	}
	mode := private
	if mode == "" {
		mode = policy.PrivateKeepRunning
	}
	pol.PrivateRecovery, pol.PrivateRecoveryFrom = mode, "integration test"
	now := time.Now().UTC()
	in := action.PlanInput{
		Finding:  domain.Finding{ID: 1, Type: ftype, State: domain.FindingOpen},
		Transfer: stored, Queue: rows,
		Policy: pol,
		Origin: action.OriginAutomatic, ClientKind: clientKind, ClientProbe: cp, ManagerKind: mgr.Kind(), ManagerProbe: mp,
		PollInterval: 10 * time.Minute, Now: now,
		// What this test establishes, for exactly these versions.
		Tested: []action.TestedWorkflow{{Workflow: wf, Manager: mgr.Kind(), ManagerVersion: strings.TrimPrefix(mp.Version, "v"),
			Client: clientKind, ClientVersion: strings.TrimPrefix(cp.Version, "v")}},
	}
	p, np := action.Plan(in)
	if np != nil {
		t.Fatal(np.Reason)
	}
	if priv, known := tr.Private.Get(); wf == policy.WorkflowMetadataRecovery {
		if known {
			t.Logf("privacy known (%v) before metadata on %s %s", priv, clientKind, cp.Version)
		}
		if !p.UnknownPrivacy && !known {
			t.Fatal("unknown privacy accepted without recording the opt-in")
		}
	} else if private != "" {
		// Known private, or unknown where the client cannot report privacy
		// (qBittorrent 4.x), which is treated as private.
		if known && !priv {
			t.Fatalf("private fixture reported privacy %v on %s %s", tr.Private, clientKind, cp.Version)
		}
		if p.PrivateMode != private || len(p.Operations) != 1 || p.Operations[0].Op != "manager.queue.fail" {
			t.Fatalf("private plan: mode %q operations %+v", p.PrivateMode, p.Operations)
		}
	} else if !known {
		// qBittorrent 4.x does not report privacy: treated as private
		// (user decision 2026-11-17), so the default keeps it running.
		if p.PrivateMode != policy.PrivateKeepRunning || len(p.Operations) != 1 || p.Operations[0].Op != "manager.queue.fail" {
			t.Fatalf("unknown privacy plan: mode %q operations %+v blockers %+v", p.PrivateMode, p.Operations, p.Blockers())
		}
		t.Logf("privacy unknown on %s %s: treated as private, kept running", clientKind, cp.Version)
		private = policy.PrivateKeepRunning
	} else if priv {
		t.Fatal("synthetic release unexpectedly private")
	}
	if !p.Eligible() {
		t.Fatalf("proposal not eligible: %+v", p.Blockers())
	}
	ex := workflow.FromConnections(conns.Clients, conns.Managers)
	// What the live check will see of the manager's redownload settings and
	// this grab's release source (real values, logged as evidence).
	if lv, err := ex.Live(ctx, *p); err == nil {
		if lv.Redownload != nil {
			ia := "not reported"
			if lv.Redownload.FromInteractive != nil {
				ia = fmt.Sprint(*lv.Redownload.FromInteractive)
			}
			t.Logf("live redownload: autoRedownloadFailed=%v fromInteractiveSearch=%s grab releaseSource=%q", lv.Redownload.Failed, ia, lv.Redownload.GrabSource)
		} else {
			t.Logf("live redownload not read: %s", lv.RedownloadErr)
		}
	}
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

	wantState, wantDisp := domain.StatePaused, store.RetainedStopped
	if private == policy.PrivateKeepRunning {
		wantState, wantDisp = domain.StateDownloading, store.RetainedRunning // never stopped
	}
	// Stopped or left running, the torrent is counted as retained.
	if ret, _ := st.RetainedTransfers(ctx, false, 0); len(ret) != 1 || ret[0].Disposition != wantDisp || ret[0].NativeID != tr.NativeID {
		t.Errorf("retained %+v, want one %s record", ret, wantDisp)
	}
	after, hist, searches, replacement := verifyRecovered(t, a, mgr, e, *row, tr.Category, digest, hist0, start, searchName, wantState)
	// Housekeeparr itself recorded exactly that replacement search as a
	// separate outcome, right after the recovery.
	requireSearchOutcome(t, st, res.IntentID, searchName, replacement)
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

// verifyRecovered checks, independently of Housekeeparr's own verification
// and separately for each outcome, that a recovery took effect on the real
// instances: the queue row stays absent after two refreshes, exactly this
// release is blocklisted with one new downloadFailed and no new grab, the
// torrent is still registered, stopped, in the same category with a
// byte-identical payload, and the manager queued its own replacement search.
func verifyRecovered(t *testing.T, a arrAPI, mgr recoveryManager, e entry, row domain.QueueItem, category domain.Signal[string],
	digest string, hist0 []string, start time.Time, searchName string, wantState domain.StateClass) (domain.Transfer, []string, []arrCommand, int64) {
	t.Helper()
	ctx := context.Background()
	var replacement int64
	statuses := []string{a.command("RefreshMonitoredDownloads")}
	time.Sleep(3 * time.Second)
	statuses = append(statuses, a.command("RefreshMonitoredDownloads"))
	time.Sleep(3 * time.Second)
	if a.row(nil, e.Hash) != nil {
		t.Errorf("queue row returned after refreshes %v", statuses)
	}
	// By the release's own title: the manager blocklists under it, while the
	// queue row of a magnet without metadata carries the client's placeholder
	// name on rTorrent ("<HASH>.meta").
	blocked, err := mgr.Blocklisted(ctx, arr.BlocklistMatch{SourceTitle: e.Name, Since: start.Add(-time.Minute),
		SeriesID: row.SeriesID, EpisodeID: row.EpisodeID, MovieID: row.MovieID})
	if err != nil || !blocked {
		t.Errorf("release not blocklisted: %v %v", blocked, err)
	}
	hist := a.history(e.Hash)
	if count(hist, "downloadFailed") != count(hist0, "downloadFailed")+1 || count(hist, "grabbed") != count(hist0, "grabbed") {
		t.Errorf("history %v → %v", hist0, hist)
	}
	after, ok := clientState(t, e.Hash)
	if !ok || after.StateClass != wantState || after.Category != category {
		t.Errorf("client after: present=%v %s/%s category %v (was %v)", ok, after.NativeState, after.StateClass, after.Category, category)
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
	// The adapter's command reader sees the same search, scoped to this
	// content by the command body (what search-acceptance tracking uses).
	if cr, ok := mgr.(interface {
		Commands(context.Context) ([]arr.Command, error)
	}); ok {
		cmds, err := cr.Commands(ctx)
		if err != nil {
			t.Fatalf("commands: %v", err)
		}
		matched := false
		for _, c := range cmds {
			if c.Name != searchName || c.Queued.Before(start.Add(-time.Minute)) {
				continue
			}
			if (row.EpisodeID > 0 && slices.Contains(c.EpisodeIDs, row.EpisodeID)) || (row.MovieID > 0 && slices.Contains(c.MovieIDs, row.MovieID)) {
				t.Logf("adapter command: %+v", c)
				// The manager-native redownload search (the harness's own
				// grab search is "manual" and precedes the request).
				if c.Trigger == "unspecified" && !c.Queued.Before(start) {
					if matched {
						t.Errorf("more than one manager-native %s for this content after the request", searchName)
					}
					matched, replacement = true, c.ID
				}
			}
		}
		if !matched {
			t.Errorf("the adapter's command list has no %s for this content: %+v", searchName, cmds)
		}
		// Raw response, for sanitized contract fixtures.
		_, raw := a.do(http.MethodGet, "/api/v3/command", nil, nil)
		t.Logf("raw /api/v3/command: %s", raw)
	} else {
		t.Errorf("%T has no command reader", mgr)
	}
	return after, hist, searches, replacement
}

// requireSearchOutcome checks the replacement-search outcome Housekeeparr
// recorded for an intent: the manager-native search was seen accepted,
// running, or completed (it completes within seconds on these instances).
func requireSearchOutcome(t *testing.T, st *store.Store, intentID int64, searchName string, wantID int64) {
	t.Helper()
	oc, err := st.Outcomes(context.Background(), []int64{intentID})
	if err != nil {
		t.Fatal(err)
	}
	s := oc[intentID].Search
	switch s.State {
	case store.SearchAccepted, store.SearchRunning, store.SearchCompleted:
	default:
		t.Fatalf("action #%d: replacement search outcome %+v, want it observed", intentID, s)
	}
	if s.Name != searchName || s.CommandID == nil || *s.CommandID != wantID || s.AfterID == nil {
		t.Fatalf("action #%d: replacement search outcome %+v, want %s command %d attributed against a baseline", intentID, s, searchName, wantID)
	}
	t.Logf("action #%d: replacement search recorded: %s — %s", intentID, s.State, s.Note)
}

// qbtReannounce asks qBittorrent to announce a torrent now (harness only).
func qbtReannounce(t *testing.T, hash string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	base := env(t, "HK_IT_QBT_URL")
	resp, err := hc.PostForm(base+"/api/v2/auth/login", url.Values{"username": {env(t, "HK_IT_QBT_USER")}, "password": {env(t, "HK_IT_QBT_PASS")}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = hc.PostForm(base+"/api/v2/torrents/reannounce", url.Values{"hashes": {hash}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
