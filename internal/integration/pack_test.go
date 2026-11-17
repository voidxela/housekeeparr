//go:build integration

package integration

// TestSonarrPartialPackProbe records, on a released Sonarr, how a completed
// season pack that contains only some of the season's expected episodes is
// represented after import (SPEC §8), and what the ignore-path queue
// removal does to it. It is evidence for designing partial-pack handling,
// not a Housekeeparr workflow: the only write is the removal it measures,
// sent with every flag explicit (removeFromClient=false, blocklist=false,
// skipRedownload=true, changeCategory=false).

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

func TestSonarrPartialPackProbe(t *testing.T) {
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
			t.Logf("waiting for import: client %s/%s; episodes E01=%v E02=%v", tr.NativeState, tr.StateClass, eps[1].HasFile, eps[2].HasFile)
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

	// The ignore path, every flag explicit.
	q := url.Values{"removeFromClient": {"false"}, "blocklist": {"false"}, "skipRedownload": {"true"}, "changeCategory": {"false"}}
	code, body := a.do(http.MethodDelete, fmt.Sprintf("/api/v3/queue/%d", rows[0].ID), q, nil)
	if code != 200 {
		t.Fatalf("ignore removal: HTTP %d %s", code, body)
	}
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
