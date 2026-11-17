//go:build integration

package integration

// TestSonarrPartialPackWorkflow records, on a released Sonarr, how a
// completed season pack that contains only some of the season's expected
// episodes is represented after import (SPEC §8), then lets Housekeeparr's
// production path clear the stale association and checks every effect
// independently: rows gone durably, downloadIgnored recorded, the torrent,
// its payload, and the imported files untouched.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	"fmt"
	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

type packRow struct {
	ID             int64  `json:"id"`
	DownloadID     string `json:"downloadId"`
	EpisodeID      int64  `json:"episodeId"`
	EpisodeHasFile bool   `json:"episodeHasFile"`
	State          string `json:"trackedDownloadState"`
	Status         string `json:"trackedDownloadStatus"`
	Messages       []struct {
		Title    string   `json:"title"`
		Messages []string `json:"messages"`
	} `json:"statusMessages"`
}

type packEpisode struct {
	ID            int64 `json:"id"`
	EpisodeNumber int   `json:"episodeNumber"`
	HasFile       bool  `json:"hasFile"`
	Monitored     bool  `json:"monitored"`
}

func TestSonarrPartialPackWorkflow(t *testing.T) {
	mm := managedManifest(t)
	e := mm["tv-s02-pack"]
	if e.Hash == "" {
		t.Fatal("tv-s02-pack missing from the manifest")
	}
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	root := env(t, "HK_IT_WORK") + "/library/tv"
	prepareManager(t, a, root, []int{5000, 5040})
	sid := ensureSeries(t, a)
	a.commandBody(map[string]any{"name": "RefreshSeries", "seriesId": sid})
	episodes := func() map[int]packEpisode {
		var eps []packEpisode
		a.mustJSON(http.MethodGet, "/api/v3/episode", url.Values{"seriesId": {fmt.Sprint(sid)}, "seasonNumber": {"2"}}, nil, &eps)
		out := map[int]packEpisode{}
		for _, ep := range eps {
			out[ep.EpisodeNumber] = ep
		}
		return out
	}
	var eps map[int]packEpisode
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(3 * time.Second) {
		if eps = episodes(); len(eps) == 3 || time.Now().After(deadline) {
			break
		}
	}
	if len(eps) != 3 {
		t.Fatalf("season 2 episodes %+v", eps)
	}
	var ids []int64
	for _, ep := range eps {
		ids = append(ids, ep.ID)
	}
	a.mustJSON(http.MethodPut, "/api/v3/episode/monitor", nil, map[string]any{"episodeIds": ids, "monitored": true}, nil)

	waitGrab(t, a, e, map[string]any{"name": "SeasonSearch", "seriesId": sid, "seasonNumber": 2},
		url.Values{"seriesId": {fmt.Sprint(sid)}, "seasonNumber": {"2"}})

	// Import of the two present episodes.
	for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(5 * time.Second) {
		eps = episodes()
		if eps[1].HasFile && eps[2].HasFile {
			break
		}
		if time.Now().After(deadline) {
			rows := packRows(a, e.Hash)
			t.Fatalf("E01/E02 not imported: episodes %+v; queue rows %s", eps, jsonText(rows))
		}
		if tr, ok := clientState(t, e.Hash); ok {
			t.Logf("waiting for import: client %s/%s verified %v of %v; episodes E01=%v E02=%v", tr.NativeState, tr.StateClass, tr.VerifiedBytes, tr.WantedBytes,
				eps[1].HasFile, eps[2].HasFile)
			if managerClientKind() == "qbittorrent" && tr.StateClass != domain.StateComplete {
				t.Logf("qBittorrent paths: %s (expected payload under %s)", qbtPaths(t, e.Hash), env(t, "HK_IT_QBT_SAVE"))
			}
		}
	}
	if eps[3].HasFile {
		t.Fatal("E03 has a file, but the pack does not contain it")
	}
	// Let Sonarr finish processing the tracked download, then record it.
	a.command("RefreshMonitoredDownloads")
	time.Sleep(10 * time.Second)
	rows := packRows(a, e.Hash)
	t.Logf("queue rows after import (one per expected episode): %s", jsonText(rows))
	if len(rows) == 0 {
		t.Fatal("the pack left the queue after a partial import; the stale association to clear does not exist on this version")
	}
	hist := packHistory(a, e.Hash)
	t.Logf("history for the download: %s", jsonText(hist))
	imported := map[int64]bool{}
	for _, h := range hist {
		if h.EventType == "downloadFolderImported" {
			imported[h.EpisodeID] = true
		}
	}
	if !imported[eps[1].ID] || !imported[eps[2].ID] || imported[eps[3].ID] {
		t.Fatalf("import history %v does not match E01/E02 imported, E03 missing (ids %d %d %d)", imported, eps[1].ID, eps[2].ID, eps[3].ID)
	}
	tr0, ok := clientState(t, e.Hash)
	if !ok || tr0.StateClass != domain.StateComplete {
		t.Fatalf("pack not complete in the client: %v %s", ok, tr0.StateClass)
	}
	digest := payloadDigest(t, e.asEntry())

	// Housekeeparr's own workflow clears the association: the real engine
	// raises the partial-pack finding (import_settle shortened to 1m), and
	// automatic dispatch plans, revalidates with live import evidence, and
	// sends the ignore-path removal through the coordinator.
	clearPack(t, sid, e)
	gone := len(packRows(a, e.Hash)) == 0
	status := a.command("RefreshMonitoredDownloads")
	time.Sleep(10 * time.Second)
	after := packRows(a, e.Hash)
	hist2 := packHistory(a, e.Hash)
	ignored := 0
	for _, h := range hist2 {
		if h.EventType == "downloadIgnored" {
			ignored++
		}
	}
	t.Logf("after ignore: rows gone immediately=%v; after RefreshMonitoredDownloads (%s) rows=%d; downloadIgnored events=%d",
		gone, status, len(after), ignored)
	if !gone || len(after) != 0 || ignored == 0 {
		t.Fatalf("ignore not durable: gone=%v after=%s ignored=%d", gone, jsonText(after), ignored)
	}
	// Preservation: torrent registered and complete, payload and imports intact.
	tr, ok := clientState(t, e.Hash)
	if !ok || tr.StateClass != domain.StateComplete {
		t.Fatalf("torrent after ignore: %v %s", ok, tr.StateClass)
	}
	if payloadDigest(t, e.asEntry()) != digest {
		t.Fatal("payload changed")
	}
	eps = episodes()
	if !eps[1].HasFile || !eps[2].HasFile || eps[3].HasFile {
		t.Fatalf("episode files changed: %+v", eps)
	}
	var lib []string
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(p, "S02E0") {
			lib = append(lib, filepath.Base(p))
		}
		return nil
	})
	t.Logf("library files for season 2: %v", lib)
	if len(lib) != 2 {
		t.Fatalf("library files %v", lib)
	}
}

func packRows(a arrAPI, hash string) []packRow {
	a.t.Helper()
	var page struct {
		Records []packRow `json:"records"`
	}
	a.mustJSON(http.MethodGet, "/api/v3/queue", url.Values{"page": {"1"}, "pageSize": {"200"}}, nil, &page)
	var out []packRow
	for _, r := range page.Records {
		if strings.EqualFold(r.DownloadID, hash) {
			out = append(out, r)
		}
	}
	return out
}

type packHist struct {
	EpisodeID int64  `json:"episodeId"`
	EventType string `json:"eventType"`
}

func packHistory(a arrAPI, hash string) []packHist {
	a.t.Helper()
	var page struct {
		Records []packHist `json:"records"`
	}
	// History filters by the exact download ID string the client reported.
	for _, id := range []string{strings.ToUpper(hash), strings.ToLower(hash)} {
		a.mustJSON(http.MethodGet, "/api/v3/history", url.Values{"downloadId": {id}, "pageSize": {"100"}}, nil, &page)
		if len(page.Records) > 0 {
			break
		}
	}
	return page.Records
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// qbtPaths returns a torrent's save and content paths (harness diagnostics).
func qbtPaths(t *testing.T, hash string) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	base := env(t, "HK_IT_QBT_URL")
	resp, err := hc.PostForm(base+"/api/v2/auth/login", url.Values{"username": {env(t, "HK_IT_QBT_USER")}, "password": {env(t, "HK_IT_QBT_PASS")}})
	if err != nil {
		return err.Error()
	}
	resp.Body.Close()
	resp, err = hc.Get(base + "/api/v2/torrents/info?hashes=" + hash)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	var rows []struct {
		SavePath    string  `json:"save_path"`
		ContentPath string  `json:"content_path"`
		Progress    float64 `json:"progress"`
		State       string  `json:"state"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rows)
	return jsonText(rows)
}

// clearPack runs the engine and automatic dispatch in-process until the
// partial-pack action is journaled, and requires it to succeed.
func clearPack(t *testing.T, sid int, e managedEntry) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	dcMap, clientBlock := managerTOMLConfig(t)
	src := "version = 1\nmode = \"automatic\"\n[polling]\njitter = \"0s\"\n[policy]\nimport_settle = \"1m\"\n[automation.workflows]\npartial_pack = true\n" +
		"[[sonarr]]\nid = \"sonarr\"\nurl = \"" + env(t, "HK_IT_SONARR_URL") + "\"\napi_key = \"env:HK_IT_SONARR_KEY\"\n" + dcMap + "\n" + clientBlock
	cfg, _, err := config.Parse([]byte(src), filepath.Join(dir, "h.toml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, _ := cfg.ResolvedSecrets(nil)
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
	var mp *adapter.Probe
	svc := app.New(st, cfg, nil, eng)
	svc.SetExecutor(workflow.FromConnections(conns.Clients, conns.Managers))
	saved := action.Tested
	defer func() { action.Tested = saved }()
	deadline := time.Now().Add(4 * time.Minute)
	for i := 1; ; i++ {
		rep := eng.Cycle(ctx, nil)
		if mp == nil {
			recs, _ := st.Connections(ctx)
			for _, c := range recs {
				if c.ID == "sonarr" && c.Probe != nil {
					mp = c.Probe
					// What this test establishes, for exactly this version.
					var cv string
					for _, c2 := range recs {
						if c2.Kind == managerClientKind() && c2.Probe != nil {
							cv = strings.TrimPrefix(c2.Probe.Version, "v")
						}
					}
					action.Tested = []action.TestedWorkflow{{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: strings.TrimPrefix(mp.Version, "v"),
						Client: adapter.Kind(managerClientKind()), ClientVersion: cv}}
				}
			}
		}
		dr, err := svc.DispatchAutomatic(ctx, rep.Fresh())
		if err != nil {
			t.Fatal(err)
		}
		fs, _ := st.Findings(ctx, store.FindingFilter{Type: domain.FindingPartialPack})
		acts, _ := st.Intents(ctx, store.IntentFilter{})
		for _, ev := range dr.Evaluations {
			t.Logf("cycle %d: finding #%d %s: %s", i, ev.FindingID, ev.Result, ev.Detail)
		}
		if len(acts) > 0 {
			if acts[0].Workflow != string(policy.WorkflowPartialPack) || acts[0].State != store.IntentSucceeded {
				b, _ := json.Marshal(acts[0].Steps)
				t.Fatalf("action %+v steps %s", acts[0], b)
			}
			t.Logf("partial-pack action #%d succeeded after %d cycle(s); %d finding(s)", acts[0].ID, i, len(fs))
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no partial-pack action in time (%d findings)", len(fs))
		}
		time.Sleep(20 * time.Second)
	}
}
