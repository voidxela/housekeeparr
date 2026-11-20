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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	st := a.commandBody(map[string]any{"name": "EpisodeSearch", "episodeIds": []int64{searchEp}})
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		if len(a.episodeRows(me.Hash)) > 0 {
			t.Logf("%s: grabbed by EpisodeSearch (%s)", me.Key, st)
			return
		}
	}
	code, b := a.do(http.MethodGet, "/api/v3/release", url.Values{"episodeId": {fmt.Sprint(searchEp)}}, nil)
	var rels []struct {
		GUID       string   `json:"guid"`
		IndexerID  int64    `json:"indexerId"`
		Title      string   `json:"title"`
		Rejected   bool     `json:"rejected"`
		Rejections []string `json:"rejections"`
	}
	if code != 200 || json.Unmarshal(b, &rels) != nil {
		t.Fatalf("%s: interactive search: HTTP %d %s", me.Key, code, truncate(b))
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
