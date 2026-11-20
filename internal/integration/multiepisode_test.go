//go:build integration

package integration

// Multi-episode queue semantics on a released Sonarr: one release (one
// file) covering two episodes of the fictional season 3. Recorded before
// the planner may fail such a grab (SPEC §7.3, plan item 6): what the queue
// shows per episode, what DELETE /api/v3/queue/{id} with blocklist=true on
// one episode's row does to the whole download, which replacement searches
// the manager queues, and that the torrent and payload are preserved. Run
// with mixed monitoring and with one episode that already has a library
// file.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

type episodeRow struct {
	ID         int64  `json:"id"`
	EpisodeID  int64  `json:"episodeId"`
	SeriesID   int64  `json:"seriesId"`
	DownloadID string `json:"downloadId"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	State      string `json:"trackedDownloadState"`
	TDStatus   string `json:"trackedDownloadStatus"`
}

func (a arrAPI) episodeRows(hash string) []episodeRow {
	a.t.Helper()
	code, b := a.do(http.MethodGet, "/api/v3/queue", url.Values{"page": {"1"}, "pageSize": {"200"}, "includeUnknownSeriesItems": {"true"}}, nil)
	if code != 200 {
		a.t.Fatalf("queue: HTTP %d %s", code, b)
	}
	var pg struct {
		Records []episodeRow `json:"records"`
	}
	if err := json.Unmarshal(b, &pg); err != nil {
		a.t.Fatal(err)
	}
	var out []episodeRow
	for _, r := range pg.Records {
		if strings.EqualFold(r.DownloadID, hash) {
			out = append(out, r)
		}
	}
	return out
}

type commandRecord struct {
	ID      int64     `json:"id"`
	Name    string    `json:"name"`
	Trigger string    `json:"trigger"`
	Status  string    `json:"status"`
	Queued  time.Time `json:"queued"`
	Message string    `json:"message"`
	Body    struct {
		EpisodeIDs   []int64 `json:"episodeIds"`
		SeriesID     int64   `json:"seriesId"`
		SeasonNumber *int    `json:"seasonNumber"`
	} `json:"body"`
}

func (a arrAPI) commandsSince(since time.Time) []commandRecord {
	a.t.Helper()
	var cs []commandRecord
	a.mustJSON(http.MethodGet, "/api/v3/command", nil, nil, &cs)
	var out []commandRecord
	for _, c := range cs {
		if strings.Contains(c.Name, "Search") && !c.Queued.Before(since) {
			out = append(out, c)
		}
	}
	return out
}

type historyRecord struct {
	ID         int64             `json:"id"`
	EpisodeID  int64             `json:"episodeId"`
	EventType  string            `json:"eventType"`
	DownloadID string            `json:"downloadId"`
	Data       map[string]string `json:"data"`
}

func (a arrAPI) historyRecords(hash string) []historyRecord {
	a.t.Helper()
	code, b := a.do(http.MethodGet, "/api/v3/history", url.Values{"page": {"1"}, "pageSize": {"100"}, "downloadId": {strings.ToUpper(hash)}}, nil)
	if code != 200 {
		a.t.Fatalf("history: HTTP %d %s", code, b)
	}
	var pg struct {
		Records []historyRecord `json:"records"`
	}
	_ = json.Unmarshal(b, &pg)
	var out []historyRecord
	for _, r := range pg.Records {
		if strings.EqualFold(r.DownloadID, hash) {
			out = append(out, r)
		}
	}
	return out
}

type blocklistRecord struct {
	ID          int64   `json:"id"`
	EpisodeIDs  []int64 `json:"episodeIds"`
	SourceTitle string  `json:"sourceTitle"`
	Protocol    string  `json:"protocol"`
	Indexer     string  `json:"indexer"`
}

func (a arrAPI) blocklistRecords(title string) []blocklistRecord {
	a.t.Helper()
	var pg struct {
		Records []blocklistRecord `json:"records"`
	}
	a.mustJSON(http.MethodGet, "/api/v3/blocklist", url.Values{"page": {"1"}, "pageSize": {"200"}}, nil, &pg)
	var out []blocklistRecord
	for _, r := range pg.Records {
		if r.SourceTitle == title {
			out = append(out, r)
		}
	}
	return out
}

type episodeInfo struct {
	ID            int64 `json:"id"`
	EpisodeNumber int   `json:"episodeNumber"`
	SeasonNumber  int   `json:"seasonNumber"`
	Monitored     bool  `json:"monitored"`
	HasFile       bool  `json:"hasFile"`
}

func (a arrAPI) episode(id int64) episodeInfo {
	a.t.Helper()
	var e episodeInfo
	a.mustJSON(http.MethodGet, fmt.Sprintf("/api/v3/episode/%d", id), nil, nil, &e)
	return e
}

func (a arrAPI) setMonitored(ids []int64, on bool) {
	a.t.Helper()
	a.mustJSON(http.MethodPut, "/api/v3/episode/monitor", nil, map[string]any{"episodeIds": ids, "monitored": on}, nil)
}

// multiEpisodeObservation is what the probe records for one scenario; with
// --capture it is written, sanitized, to testdata/captured.
type multiEpisodeObservation struct {
	Scenario        string            `json:"scenario"`
	SonarrVersion   string            `json:"sonarr_version"`
	Episodes        []episodeInfo     `json:"episodes_before"`
	GrabSource      string            `json:"grab_release_source"`
	RowsBefore      []episodeRow      `json:"queue_rows_before"`
	Deleted         int64             `json:"deleted_queue_id"`
	DeleteFlags     string            `json:"delete_flags"`
	DeleteStatus    int               `json:"delete_http_status"`
	RowsImmediately []episodeRow      `json:"queue_rows_immediately_after"`
	RowsAfter       []episodeRow      `json:"queue_rows_after_two_refreshes"`
	Blocklist       []blocklistRecord `json:"blocklist_records_for_release"`
	History         []historyRecord   `json:"history_for_download"`
	Searches        []commandRecord   `json:"search_commands_after"`
	ClientBefore    string            `json:"client_before"`
	ClientAfter     string            `json:"client_after"`
	PayloadSame     bool              `json:"payload_byte_identical"`
}

func TestSonarrMultiEpisodeQueueSemantics(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	ep := map[int]int64{}
	for n := 1; n <= 6; n++ {
		ep[n] = int64(episodeID(t, a, sid, 3, n))
	}
	// Leave season 3 unmonitored afterwards so later tests' wanted lists
	// are unchanged.
	t.Cleanup(func() { a.setMonitored([]int64{ep[1], ep[2], ep[3], ep[4], ep[5], ep[6]}, false) })

	t.Run("both monitored", func(t *testing.T) {
		probeMultiEpisode(t, a, mm["tv-s03e01e02-multi"], "both-monitored", ep[1], []int64{ep[1], ep[2]})
	})
	t.Run("second unmonitored", func(t *testing.T) {
		a.setMonitored([]int64{ep[4]}, false)
		probeMultiEpisode(t, a, mm["tv-s03e03e04-multi"], "second-unmonitored", ep[3], []int64{ep[3], ep[4]})
	})
	t.Run("first already has a file", func(t *testing.T) {
		var series struct {
			Path string `json:"path"`
		}
		a.mustJSON(http.MethodGet, fmt.Sprintf("/api/v3/series/%d", sid), nil, nil, &series)
		seed := filepath.Join(filepath.Dir(env(t, "HK_IT_MANIFEST")), "library-seed", "Housekeeparr.Fixture.Show.S03E05.720p.HDTV.x264-HKFIX.mkv")
		b, err := os.ReadFile(seed)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(series.Path, "Season 03", filepath.Base(seed))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(90 * time.Second); !a.episode(ep[5]).HasFile; {
			a.commandBody(map[string]any{"name": "RescanSeries", "seriesId": sid})
			if time.Now().After(deadline) {
				t.Fatalf("S03E05 never picked up the library file %s", dst)
			}
			time.Sleep(2 * time.Second)
		}
		probeMultiEpisode(t, a, mm["tv-s03e05e06-multi"], "first-has-file", ep[6], []int64{ep[5], ep[6]})
	})
}

func probeMultiEpisode(t *testing.T, a arrAPI, me managedEntry, scenario string, searchEp int64, covered []int64) {
	t.Helper()
	e := me.asEntry()
	obs := multiEpisodeObservation{Scenario: scenario, SonarrVersion: os.Getenv("HK_IT_SONARR_VERSION")}
	for _, id := range covered {
		obs.Episodes = append(obs.Episodes, a.episode(id))
	}
	grabMulti(t, a, me, searchEp)
	settleIncomplete(t, me)
	// Every episode the release covers has its own row before the change.
	var rows []episodeRow
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(2 * time.Second) {
		rows = a.episodeRows(e.Hash)
		if len(rows) >= len(covered) || time.Now().After(deadline) {
			break
		}
	}
	obs.RowsBefore = rows
	for _, h := range a.historyRecords(e.Hash) {
		if h.EventType == "grabbed" {
			obs.GrabSource = h.Data["releaseSource"]
		}
	}
	t.Logf("%s: %d queue rows before: %+v (grab releaseSource %q)", scenario, len(rows), rows, obs.GrabSource)
	if len(rows) == 0 {
		t.Fatalf("%s: no queue rows", scenario)
	}
	before, ok := clientState(t, e.Hash)
	if !ok {
		t.Fatal("torrent not in client")
	}
	obs.ClientBefore = fmt.Sprintf("%s/%s cat=%v", before.NativeState, before.StateClass, before.Category)
	digest := payloadDigest(t, e)

	// Fail one episode's row, every flag explicit, as the planner would.
	flags := blocklistFlags()
	flags.Set("skipRedownload", "false")
	target := rows[0]
	start := time.Now().UTC().Add(-time.Second)
	code, body := a.do(http.MethodDelete, fmt.Sprintf("/api/v3/queue/%d", target.ID), flags, nil)
	obs.Deleted, obs.DeleteFlags, obs.DeleteStatus = target.ID, flags.Encode(), code
	t.Logf("%s: DELETE /api/v3/queue/%d (episode %d)?%s → HTTP %d %s", scenario, target.ID, target.EpisodeID, flags.Encode(), code, strings.TrimSpace(string(body)))
	if code != 200 {
		t.Fatalf("%s: removal not accepted", scenario)
	}
	time.Sleep(2 * time.Second)
	obs.RowsImmediately = a.episodeRows(e.Hash)
	for i := 0; i < 2; i++ {
		a.command("RefreshMonitoredDownloads")
		time.Sleep(3 * time.Second)
	}
	obs.RowsAfter = a.episodeRows(e.Hash)
	// The replacement search is queued asynchronously after the failure.
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(2 * time.Second) {
		obs.Searches = a.commandsSince(start)
		if len(obs.Searches) > 0 || time.Now().After(deadline) {
			break
		}
	}
	time.Sleep(5 * time.Second) // a second search, if any, would follow promptly
	obs.Searches = a.commandsSince(start)
	obs.Blocklist = a.blocklistRecords(e.Name)
	obs.History = a.historyRecords(e.Hash)
	after, ok := clientState(t, e.Hash)
	obs.ClientAfter = fmt.Sprintf("present=%v %s/%s cat=%v", ok, after.NativeState, after.StateClass, after.Category)
	obs.PayloadSame = payloadDigest(t, e) == digest
	for i := range obs.History {
		obs.History[i].Data = sanitizeHistoryData(obs.History[i].Data)
	}
	j, _ := json.MarshalIndent(obs, "", " ")
	t.Logf("%s observation:\n%s", scenario, j)
	if dir := os.Getenv("HK_IT_CAPTURE_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sonarr-multi-episode-"+scenario+".json"), append(j, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The semantics observed on Sonarr 4.0.20.3014 (docs/compatibility.md,
	// "Multi-episode queue semantics"), which the planner relies on: one row
	// per episode; failing one row fails the whole download, durably; one
	// blocklist entry for every episode; one downloadFailed per episode and
	// no new grab; exactly one replacement EpisodeSearch for every episode.
	want := slices.Sorted(slices.Values(covered))
	var rowEps []int64
	for _, r := range rows {
		rowEps = append(rowEps, r.EpisodeID)
	}
	if !slices.Equal(slices.Sorted(slices.Values(rowEps)), want) {
		t.Errorf("%s: rows before for episodes %v, want %v", scenario, rowEps, want)
	}
	if len(obs.RowsImmediately) != 0 || len(obs.RowsAfter) != 0 {
		t.Errorf("%s: rows left after failing one: immediately %+v, after refreshes %+v", scenario, obs.RowsImmediately, obs.RowsAfter)
	}
	if len(obs.Blocklist) != 1 || !slices.Equal(slices.Sorted(slices.Values(obs.Blocklist[0].EpisodeIDs)), want) {
		t.Errorf("%s: blocklist %+v, want one entry for episodes %v", scenario, obs.Blocklist, want)
	}
	failed, grabbed := 0, 0
	for _, h := range obs.History {
		switch h.EventType {
		case "downloadFailed":
			failed++
		case "grabbed":
			grabbed++
		}
	}
	if failed != len(covered) || grabbed != len(covered) {
		t.Errorf("%s: history has %d downloadFailed and %d grabbed, want %d each", scenario, failed, grabbed, len(covered))
	}
	if len(obs.Searches) != 1 || obs.Searches[0].Name != "EpisodeSearch" || !slices.Equal(slices.Sorted(slices.Values(obs.Searches[0].Body.EpisodeIDs)), want) {
		t.Errorf("%s: replacement searches %+v, want one EpisodeSearch for %v", scenario, obs.Searches, want)
	}

	// Preservation and durability are required regardless of semantics.
	if !ok {
		t.Errorf("%s: torrent no longer registered after removeFromClient=false", scenario)
	} else if after.NativeState != before.NativeState || after.Category != before.Category {
		t.Errorf("%s: client state changed: %s → %s", scenario, obs.ClientBefore, obs.ClientAfter)
	}
	if !obs.PayloadSame {
		t.Errorf("%s: payload changed", scenario)
	}
	for _, r := range obs.RowsAfter {
		if r.ID == target.ID {
			t.Errorf("%s: the failed row %d came back after refreshes", scenario, target.ID)
		}
	}
}

// grabMulti has Sonarr grab the multi-episode release: an episode search
// first; if Sonarr rejects the release for that search, the rejection is
// logged and the release is grabbed interactively ("download anyway"),
// which is recorded as the grab's release source.
func grabMulti(t *testing.T, a arrAPI, me managedEntry, searchEp int64) {
	t.Helper()
	type candidate struct {
		GUID       string   `json:"guid"`
		IndexerID  int64    `json:"indexerId"`
		Title      string   `json:"title"`
		Rejected   bool     `json:"rejected"`
		Rejections []string `json:"rejections"`
	}
	var rels []candidate
	for attempt := 1; attempt <= 3; attempt++ {
		st := a.commandBody(map[string]any{"name": "EpisodeSearch", "episodeIds": []int64{searchEp}})
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			if len(a.episodeRows(me.Hash)) > 0 {
				t.Logf("%s: grabbed by EpisodeSearch attempt %d (%s)", me.Key, attempt, st)
				return
			}
		}
		code, b := a.do(http.MethodGet, "/api/v3/release", url.Values{"episodeId": {fmt.Sprint(searchEp)}}, nil)
		rels = nil
		if code != 200 || json.Unmarshal(b, &rels) != nil {
			t.Fatalf("%s: interactive search: HTTP %d %s", me.Key, code, truncate(b))
		}
		// An acceptable release that was not grabbed can be the manager
		// settling right after a change (see waitGrab): search again. A
		// rejected one is grabbed interactively below.
		rejected := true
		for _, r := range rels {
			if r.Title == me.Name {
				rejected = r.Rejected
			}
		}
		if rejected {
			break
		}
	}
	for _, r := range rels {
		if r.Title != me.Name {
			continue
		}
		t.Logf("%s: EpisodeSearch did not grab; interactive candidate rejected=%v %v — grabbing interactively", me.Key, r.Rejected, r.Rejections)
		a.mustJSON(http.MethodPost, "/api/v3/release", nil, map[string]any{"guid": r.GUID, "indexerId": r.IndexerID}, nil)
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			if len(a.episodeRows(me.Hash)) > 0 {
				return
			}
		}
		t.Fatalf("%s: interactive grab produced no queue rows", me.Key)
	}
	t.Fatalf("%s: release not offered for episode %d (%d candidates)", me.Key, searchEp, len(rels))
}

// sanitizeHistoryData keeps the fields the probe interprets and drops
// anything URL- or path-like.
func sanitizeHistoryData(d map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range []string{"releaseSource", "indexer", "releaseGroup", "message", "downloadClient", "downloadClientName", "protocol", "size"} {
		if v, ok := d[k]; ok && !strings.Contains(v, "://") && !strings.Contains(v, "/") {
			out[k] = v
		}
	}
	return out
}

// TestSonarrMultiEpisodeRecoveryWorkflow runs no-progress recovery of a
// stalled multi-episode release through Housekeeparr's planner,
// coordinator, executor and adapters on the released instances: one
// manager request fails the whole download; each episode gets its own lock,
// attempt reservation, cooldown and outcome; a private torrent is left
// running; the payload is unchanged byte for byte. A release with an
// unmonitored episode is refused before anything is sent. Only the stall
// diagnosis is supplied, as in runRecoveryMode.
func TestSonarrMultiEpisodeRecoveryWorkflow(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	ep := map[int]int64{}
	for n := 7; n <= 14; n++ {
		ep[n] = int64(episodeID(t, a, sid, 3, n))
	}
	t.Cleanup(func() {
		var all []int64
		for _, id := range ep {
			all = append(all, id)
		}
		a.setMonitored(all, false)
	})
	a.setMonitored([]int64{ep[7], ep[8], ep[9], ep[10], ep[11], ep[12], ep[13], ep[14]}, true)

	t.Run("both monitored", func(t *testing.T) {
		runMultiRecovery(t, a, mm["tv-s03e07e08-multi"], ep[7], []int64{ep[7], ep[8]}, "", true)
	})
	t.Run("private, left running", func(t *testing.T) {
		runMultiRecovery(t, a, mm["tv-s03e09e10-multi-private"], ep[9], []int64{ep[9], ep[10]}, policy.PrivateKeepRunning, true)
	})
	t.Run("second unmonitored is refused", func(t *testing.T) {
		// Grabbed while both are monitored; the second is unmonitored before
		// the recovery, as a user might.
		runMultiRecovery(t, a, mm["tv-s03e11e12-multi"], ep[11], []int64{ep[11], ep[12]}, "", false, func() { a.setMonitored([]int64{ep[12]}, false) })
	})
	t.Run("first already has a file", func(t *testing.T) {
		var series struct {
			Path string `json:"path"`
		}
		a.mustJSON(http.MethodGet, fmt.Sprintf("/api/v3/series/%d", sid), nil, nil, &series)
		seed := filepath.Join(filepath.Dir(env(t, "HK_IT_MANIFEST")), "library-seed", "Housekeeparr.Fixture.Show.S03E13.720p.HDTV.x264-HKFIX.mkv")
		b, err := os.ReadFile(seed)
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(series.Path, "Season 03", filepath.Base(seed))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(90 * time.Second); !a.episode(ep[13]).HasFile; time.Sleep(2 * time.Second) {
			a.commandBody(map[string]any{"name": "RescanSeries", "seriesId": sid})
			if time.Now().After(deadline) {
				t.Fatalf("S03E13 never picked up the library file %s", dst)
			}
		}
		runMultiRecovery(t, a, mm["tv-s03e13e14-multi"], ep[14], []int64{ep[13], ep[14]}, "", true)
	})
}

func runMultiRecovery(t *testing.T, a arrAPI, me managedEntry, searchEp int64, covered []int64, private policy.PrivateMode, wantSent bool, beforePlan ...func()) {
	t.Helper()
	ctx := context.Background()
	e := me.asEntry()
	grabMulti(t, a, me, searchEp)
	settleIncomplete(t, me)
	for _, f := range beforePlan {
		f()
	}
	dir := t.TempDir()
	dcMap, clientBlock := managerTOMLConfig(t)
	clientID, clientKind := map[string]string{"qbittorrent": "qbit", "transmission": "tr", "deluge": "deluge", "rtorrent": "rt"}[managerClientKind()],
		adapter.Kind(managerClientKind())
	src := "version = 1\n[polling]\njitter = \"0s\"\n[[sonarr]]\nid = \"sonarr\"\nurl = \"" + env(t, "HK_IT_SONARR_URL") +
		"\"\napi_key = \"env:HK_IT_SONARR_KEY\"\n" + dcMap + "\n" + clientBlock
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
	// Every episode's row is stored and exactly correlated.
	var stored *store.TransferRecord
	var rows []store.QueueRecord
	for i := 0; i < 3; i++ {
		for _, c := range eng.Cycle(ctx, nil).Connections {
			if !c.OK {
				t.Fatalf("cycle %s: %s", c.ID, c.Error)
			}
		}
		stored, _ = st.Transfer(ctx, clientID, e.Hash)
		rows, _ = st.QueueItems(ctx, store.QueueFilter{ClientID: clientID, NativeID: e.Hash})
		matched := 0
		for _, r := range rows {
			if r.MatchStatus == store.MatchMatched {
				matched++
			}
		}
		if stored != nil && matched == len(covered) && len(rows) == len(covered) {
			break
		}
	}
	if stored == nil || len(rows) != len(covered) {
		t.Fatalf("engine did not record one correlated row per episode: %+v %+v", stored, rows)
	}
	var cp, mp *adapter.Probe
	recs, _ := st.Connections(ctx)
	for _, c := range recs {
		switch c.ID {
		case clientID:
			cp = c.Probe
		case "sonarr":
			mp = c.Probe
		}
	}
	stored.Assessment = &policy.Assessment{Status: policy.StatusQualified, Diagnosis: policy.DiagnosisNoProgress, Headline: "supplied by the integration test",
		PolicyRevision: policy.Settings{}.Revision()}
	wf := policy.WorkflowNoProgressRecovery
	pol := policy.Effective{Mode: "automatic", Revision: "it", Automation: []policy.WorkflowPermission{{Workflow: wf, Enabled: true, From: "integration test"}},
		UnknownPrivacy: []policy.WorkflowPermission{{Workflow: wf, Enabled: true, From: "integration test"}}}
	pol.PrivateRecovery, pol.PrivateRecoveryFrom = policy.PrivateKeepRunning, "integration test"
	in := action.PlanInput{
		Finding: domain.Finding{ID: 1, Type: domain.FindingTransferStalled, State: domain.FindingOpen}, Transfer: stored, Queue: rows, Policy: pol,
		Origin: action.OriginAutomatic, ClientKind: clientKind, ClientProbe: cp, ManagerKind: adapter.KindSonarr, ManagerProbe: mp,
		PollInterval: 10 * time.Minute, Now: time.Now().UTC(),
		Tested: []action.TestedWorkflow{{Workflow: wf, Manager: adapter.KindSonarr, ManagerVersion: strings.TrimPrefix(mp.Version, "v"),
			Client: clientKind, ClientVersion: strings.TrimPrefix(cp.Version, "v"), MultiEpisode: true}},
	}
	p, np := action.Plan(in)
	if np != nil {
		t.Fatal(np.Reason)
	}
	if !p.Eligible() {
		t.Fatalf("multi-episode proposal not eligible: %+v", p.Blockers())
	}
	priv, known := stored.Latest.Private.Get()
	keepRunning := p.PrivateMode == policy.PrivateKeepRunning
	if private != "" && (!keepRunning || (known && !priv)) {
		t.Fatalf("private fixture planned as %q (privacy %v)", p.PrivateMode, stored.Latest.Private)
	}
	if private == "" && known && priv {
		t.Fatal("public fixture reported private")
	}
	if len(p.Operations) == 0 || p.Operations[0].Op != "manager.queue.fail" {
		t.Fatalf("operations %+v", p.Operations)
	}
	var want []string
	for _, id := range covered {
		want = append(want, fmt.Sprintf("episode:%d", id))
	}
	slices.Sort(want)
	if !slices.Equal(p.Planned.Content, want) {
		t.Fatalf("planned content %v, want %v", p.Planned.Content, want)
	}

	ex := workflow.FromConnections(conns.Clients, conns.Managers, cfg.ActionTiming)
	co := &action.Coordinator{Store: st, Executor: ex, Actor: "integration"}
	before, _ := clientState(t, e.Hash)
	digest := payloadDigest(t, e)
	start := time.Now().UTC().Add(-time.Second)
	res, err := co.Execute(ctx, *p)
	intent, _ := st.Intent(ctx, res.IntentID)
	b, _ := json.MarshalIndent(intent.Steps, "", " ")
	t.Logf("result %+v err=%v\nsteps %s", res, err, b)

	if !wantSent {
		// Refused live before the manager step: nothing sent, the rows and
		// the torrent untouched.
		if err == nil || res.State == store.IntentSucceeded {
			t.Fatalf("a release with an unmonitored episode was recovered: %+v %v", res, err)
		}
		for _, s := range intent.Steps {
			if s.DispatchedAt != nil {
				t.Fatalf("step %s was sent", s.Op)
			}
		}
		if !strings.Contains(err.Error(), "including unmonitored ones") && !strings.Contains(fmt.Sprint(res), "including unmonitored ones") {
			t.Errorf("refusal does not explain the unmonitored episode: %v %+v", err, res)
		}
		if n := len(a.episodeRows(e.Hash)); n != len(covered) {
			t.Errorf("%d rows after a refusal, want %d", n, len(covered))
		}
		if d := payloadDigest(t, e); d != digest {
			t.Errorf("payload changed: %s → %s", digest, d)
		}
		if bl := a.blocklistRecords(e.Name); len(bl) != 0 {
			t.Errorf("blocklisted after a refusal: %+v", bl)
		}
		return
	}
	if err != nil || res.State != store.IntentSucceeded {
		t.Fatalf("workflow did not succeed: %+v %v", res, err)
	}
	for i := 0; i < 2; i++ {
		a.command("RefreshMonitoredDownloads")
		time.Sleep(3 * time.Second)
	}
	if rows := a.episodeRows(e.Hash); len(rows) != 0 {
		t.Errorf("rows left after the recovery: %+v", rows)
	}
	if bl := a.blocklistRecords(e.Name); len(bl) != 1 || !slices.Equal(slices.Sorted(slices.Values(bl[0].EpisodeIDs)), slices.Sorted(slices.Values(covered))) {
		t.Errorf("blocklist %+v, want one entry for %v", bl, covered)
	}
	var searches []commandRecord
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(2 * time.Second) {
		searches = nil
		for _, c := range a.commandsSince(start) {
			if c.Trigger == "unspecified" {
				searches = append(searches, c)
			}
		}
		if len(searches) > 0 || time.Now().After(deadline) {
			break
		}
	}
	if len(searches) != 1 || searches[0].Name != "EpisodeSearch" || !slices.Equal(slices.Sorted(slices.Values(searches[0].Body.EpisodeIDs)), slices.Sorted(slices.Values(covered))) {
		t.Errorf("replacement searches %+v, want one EpisodeSearch for %v", searches, covered)
	}
	after, ok := clientState(t, e.Hash)
	wantState := domain.StatePaused
	if keepRunning {
		wantState = before.StateClass
	}
	if !ok || after.StateClass != wantState || after.Category != before.Category {
		t.Errorf("client after: present=%v %s/%s category %v (was %s, %v)", ok, after.NativeState, after.StateClass, after.Category, before.StateClass, before.Category)
	}
	if d := payloadDigest(t, e); d != digest {
		t.Errorf("payload changed: %s → %s", digest, d)
	}
	// Per episode: a released lock, a cooldown, a reserved attempt, and an
	// outcome followed separately.
	for _, k := range want {
		lock := action.ContentLock("sonarr", k)
		if h, _ := st.LockHolder(ctx, lock); h != 0 {
			t.Errorf("%s still held", lock)
		}
		if _, _, ok, _ := st.Cooldown(ctx, lock); !ok {
			t.Errorf("no cooldown on %s", lock)
		}
	}
	usage, _, err := st.AttemptCounts(ctx, "attempts:", 0, 0, time.Now(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range want {
		scope, found := action.AttemptScope("sonarr", k), false
		for _, u := range usage {
			found = found || u.Scope == scope && u.Used >= 1
		}
		if !found {
			t.Errorf("no attempt recorded for %s: %+v", scope, usage)
		}
	}
	oc, _ := st.Outcomes(ctx, []int64{res.IntentID})
	if o := oc[res.IntentID]; len(o.PerContent) != len(covered) {
		t.Errorf("outcome %+v, want one per episode", o)
	} else {
		t.Logf("outcome: %s — %s; search %s", o.State, o.Note, o.Search.State)
	}
}
