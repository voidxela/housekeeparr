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
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

type recoveryManager interface {
	workflow.Manager
	Probe(ctx context.Context) (adapter.Probe, error)
	Kind() adapter.Kind
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
	settle(t, e)
	runRecovery(t, a, sonarrClient(t), e, "EpisodeSearch")
}

func TestRadarrRecoveryWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	e := mm["film-e"]
	id := addMovie(t, a, e.Managed.TmdbID)
	waitGrab(t, a, e, map[string]any{"name": "MoviesSearch", "movieIds": []int{id}}, url.Values{"movieId": {fmt.Sprint(id)}})
	settle(t, e)
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
	qb := qbit(t)
	cp, err := qb.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := mgr.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := clientState(t, e.Hash)
	if !ok {
		t.Fatal("grabbed torrent not in client")
	}
	q, err := mgr.Queue(ctx)
	if err != nil || !q.Complete {
		t.Fatalf("queue: %v", err)
	}
	var row *domain.QueueItem
	for i := range q.Items {
		if strings.EqualFold(q.Items[i].DownloadID, e.Hash) {
			row = &q.Items[i]
		}
	}
	if row == nil {
		t.Fatal("no queue row for the grab")
	}
	now := time.Now().UTC()
	in := action.PlanInput{
		Finding: domain.Finding{ID: 1, Type: domain.FindingTransferStalled, State: domain.FindingOpen},
		Transfer: &store.TransferRecord{ClientID: "qbit", NativeID: tr.NativeID, Name: tr.Name, StateClass: tr.StateClass, LastSeen: now, Present: true, Latest: tr,
			Assessment: &policy.Assessment{Status: policy.StatusQualified, Diagnosis: policy.DiagnosisNoProgress, Headline: "supplied by the integration test"}},
		Queue: []store.QueueRecord{{ManagerID: mgr.ID(), ItemKey: store.QueueItemKey(*row), QueueID: row.QueueID, DownloadID: row.DownloadID,
			Present: true, MatchStatus: store.MatchMatched, Latest: *row}},
		Policy: policy.Effective{Mode: "automatic", Revision: "it", Automation: []policy.WorkflowPermission{{Workflow: policy.WorkflowNoProgressRecovery, Enabled: true, From: "integration test"}}},
		Origin: action.OriginAutomatic, ClientKind: adapter.KindQBittorrent, ClientProbe: &cp, ManagerKind: mgr.Kind(), ManagerProbe: &mp,
		PollInterval: 10 * time.Minute, Now: now,
		// What this test establishes, for exactly these versions.
		Tested: []action.TestedWorkflow{{Workflow: policy.WorkflowNoProgressRecovery, Manager: mgr.Kind(), ManagerVersion: strings.TrimPrefix(mp.Version, "v"),
			Client: adapter.KindQBittorrent, ClientVersion: strings.TrimPrefix(cp.Version, "v")}},
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

	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ex := workflow.FromConnections([]adapter.DownloadClient{qb}, []adapter.MediaManager{mgr.(adapter.MediaManager)})
	co := &action.Coordinator{Store: st, Executor: ex, Actor: "integration"}
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
