//go:build integration

package integration

// Workflow-semantics probes. These send manager write requests directly
// from the test (never through a Housekeeparr adapter, which has no write
// methods) to disposable instances, with every consequential flag explicit,
// and record what the released version actually does. They run after the
// observation tests because they change manager state.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/deluge"
	"github.com/voidxela/housekeeparr/internal/adapter/rtorrent"
	"github.com/voidxela/housekeeparr/internal/adapter/transmission"
	"github.com/voidxela/housekeeparr/internal/domain"
)

type arrAPI struct {
	t    *testing.T
	base string
	key  string
}

func (a arrAPI) do(method, path string, q url.Values, body any) (int, []byte) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	u := a.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, _ := http.NewRequest(method, u, rd)
	req.Header.Set("X-Api-Key", a.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b
}

type queueRow struct {
	ID         int64  `json:"id"`
	DownloadID string `json:"downloadId"`
	Title      string `json:"title"`
	State      string `json:"trackedDownloadState"`
}

func (a arrAPI) queue(extra url.Values) []queueRow {
	a.t.Helper()
	q := url.Values{"page": {"1"}, "pageSize": {"200"}}
	for k, v := range extra {
		q[k] = v
	}
	code, b := a.do(http.MethodGet, "/api/v3/queue", q, nil)
	if code != 200 {
		a.t.Fatalf("queue: HTTP %d %s", code, b)
	}
	var pg struct {
		Records []queueRow `json:"records"`
	}
	if err := json.Unmarshal(b, &pg); err != nil {
		a.t.Fatal(err)
	}
	return pg.Records
}

func (a arrAPI) row(extra url.Values, hash string) *queueRow {
	for _, r := range a.queue(extra) {
		if strings.EqualFold(r.DownloadID, hash) {
			r := r
			return &r
		}
	}
	return nil
}

// command posts a manager command and waits for it to finish.
func (a arrAPI) command(name string) string {
	a.t.Helper()
	code, b := a.do(http.MethodPost, "/api/v3/command", nil, map[string]any{"name": name})
	if code != 201 && code != 200 {
		a.t.Fatalf("command %s: HTTP %d %s", name, code, b)
	}
	var c struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(b, &c)
	deadline := time.Now().Add(60 * time.Second)
	for c.Status != "completed" && c.Status != "failed" && time.Now().Before(deadline) {
		time.Sleep(time.Second)
		_, b = a.do(http.MethodGet, fmt.Sprintf("/api/v3/command/%d", c.ID), nil, nil)
		_ = json.Unmarshal(b, &c)
	}
	return c.Status
}

func (a arrAPI) blocklist() []string {
	a.t.Helper()
	code, b := a.do(http.MethodGet, "/api/v3/blocklist", url.Values{"page": {"1"}, "pageSize": {"100"}}, nil)
	if code != 200 {
		a.t.Fatalf("blocklist: HTTP %d %s", code, b)
	}
	var pg struct {
		Records []struct {
			SourceTitle string `json:"sourceTitle"`
			TorrentHash string `json:"torrentInfoHash"`
		} `json:"records"`
	}
	_ = json.Unmarshal(b, &pg)
	var out []string
	for _, r := range pg.Records {
		out = append(out, r.SourceTitle)
	}
	return out
}

func (a arrAPI) history(hash string) []string {
	a.t.Helper()
	code, b := a.do(http.MethodGet, "/api/v3/history", url.Values{"page": {"1"}, "pageSize": {"50"}, "downloadId": {strings.ToUpper(hash)}}, nil)
	if code != 200 {
		return []string{fmt.Sprintf("history unavailable: HTTP %d", code)}
	}
	var pg struct {
		Records []struct {
			EventType  string `json:"eventType"`
			DownloadID string `json:"downloadId"`
		} `json:"records"`
	}
	_ = json.Unmarshal(b, &pg)
	var out []string
	for _, r := range pg.Records {
		// Filter client-side as well: the query filter is not relied on.
		if strings.EqualFold(r.DownloadID, hash) {
			out = append(out, r.EventType)
		}
	}
	return out
}

func managerClientKind() string {
	if k := os.Getenv("HK_IT_MANAGER_CLIENT"); k != "" {
		return k
	}
	return "qbittorrent"
}

func expectedManagerClientName() string {
	switch managerClientKind() {
	case "transmission":
		return "Transmission"
	case "deluge":
		return "Deluge"
	case "rtorrent":
		return "rTorrent"
	default:
		return "qBittorrent"
	}
}

func managerClient(t *testing.T) adapter.DownloadClient {
	t.Helper()
	switch managerClientKind() {
	case "qbittorrent":
		return qbit(t)
	case "transmission":
		c, err := transmission.New(transmission.Options{ID: "tr", BaseURL: env(t, "HK_IT_TR_URL"), Username: env(t, "HK_IT_TR_USER"), Password: env(t, "HK_IT_TR_PASS"), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return c
	case "deluge":
		c, err := deluge.New(deluge.Options{ID: "deluge", BaseURL: env(t, "HK_IT_DELUGE_URL"), Password: env(t, "HK_IT_DELUGE_PASS"), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return c
	case "rtorrent":
		c, err := rtorrent.New(rtorrent.Options{ID: "rt", BaseURL: env(t, "HK_IT_RT_URL"), Username: env(t, "HK_IT_RT_USER"), Password: env(t, "HK_IT_RT_PASS"), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		return c
	default:
		t.Fatalf("unsupported HK_IT_MANAGER_CLIENT %q", managerClientKind())
		return nil
	}
}

func managerTOMLConfig(t *testing.T) (string, string) {
	t.Helper()
	switch managerClientKind() {
	case "qbittorrent":
		return `download_clients = { "qBittorrent" = "qbit" }`, `[[qbittorrent]]
id = "qbit"
url = "` + env(t, "HK_IT_QBT_URL") + `"
username = "admin"
password = "env:HK_IT_QBT_PASS"
`
	case "transmission":
		return `download_clients = { "Transmission" = "tr" }`, `[[transmission]]
id = "tr"
url = "` + env(t, "HK_IT_TR_URL") + `"
username = "admin"
password = "env:HK_IT_TR_PASS"
`
	case "deluge":
		return `download_clients = { "Deluge" = "deluge" }`, `[[deluge]]
id = "deluge"
url = "` + env(t, "HK_IT_DELUGE_URL") + `"
password = "env:HK_IT_DELUGE_PASS"
`
	case "rtorrent":
		return `download_clients = { "rTorrent" = "rt" }`, `[[rtorrent]]
id = "rt"
url = "` + env(t, "HK_IT_RT_URL") + `"
username = "admin"
password = "env:HK_IT_RT_PASS"
`
	default:
		t.Fatalf("unsupported HK_IT_MANAGER_CLIENT %q", managerClientKind())
		return "", ""
	}
}

func managerPayloadDir(t *testing.T, e entry) string {
	t.Helper()
	switch managerClientKind() {
	case "qbittorrent":
		if d := os.Getenv("HK_IT_QBT_SAVE"); d != "" {
			return d
		}
		return filepath.Join(env(t, "HK_IT_WORK"), "qbt-downloads")
	case "transmission":
		cat := "tv-sonarr"
		if e.Manager == "radarr" {
			cat = "movies-radarr"
		}
		return filepath.Join(env(t, "HK_IT_TR_SAVE"), cat)
	case "deluge":
		return env(t, "HK_IT_DELUGE_SAVE")
	case "rtorrent":
		return env(t, "HK_IT_RT_SAVE")
	default:
		t.Fatalf("unsupported HK_IT_MANAGER_CLIENT %q", managerClientKind())
		return ""
	}
}

// payloadDigest hashes every payload file of a torrent in the configured
// manager download client's save directory; a missing file is recorded as such.
func payloadDigest(t *testing.T, e entry) string {
	t.Helper()
	dir := managerPayloadDir(t, e)
	var parts []string
	paths := []string{filepath.Join(dir, e.Name)}
	if len(e.Files) > 0 && !(len(e.Files) == 1 && e.Files[0] == e.Name) {
		paths = nil
		for _, f := range e.Files {
			paths = append(paths, filepath.Join(dir, e.Name, f))
		}
	}
	sort.Strings(paths)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			parts = append(parts, filepath.Base(p)+":missing")
			continue
		}
		sum := sha256.Sum256(b)
		parts = append(parts, filepath.Base(p)+":"+hex.EncodeToString(sum[:8]))
	}
	return strings.Join(parts, ",")
}

func clientState(t *testing.T, hash string) (domain.Transfer, bool) {
	t.Helper()
	c := managerClient(t)
	snap, err := c.ListTransfers(context.Background())
	if err != nil || !snap.Complete {
		t.Fatalf("%s listing: %v", expectedManagerClientName(), err)
	}
	for _, tr := range snap.Transfers {
		if tr.NativeID == hash {
			return tr, true
		}
	}
	return domain.Transfer{}, false
}

// probeRemoval removes one queue row with explicit flags, checks what is
// preserved, and checks the recorded durability semantics.
func probeRemoval(t *testing.T, a arrAPI, extra url.Values, e entry, flags url.Values) {
	t.Helper()
	row := a.row(extra, e.Hash)
	if row == nil {
		t.Fatalf("%s: no queue row to probe", e.Key)
	}
	before, ok := clientState(t, e.Hash)
	if !ok {
		t.Fatalf("%s: torrent not in client before probe", e.Key)
	}
	digest := payloadDigest(t, e)
	blBefore := len(a.blocklist())
	code, body := a.do(http.MethodDelete, fmt.Sprintf("/api/v3/queue/%d", row.ID), flags, nil)
	t.Logf("%s: DELETE /api/v3/queue/%d?%s → HTTP %d %s", e.Key, row.ID, flags.Encode(), code, strings.TrimSpace(string(body)))
	if code != 200 {
		t.Errorf("%s: removal not accepted (HTTP %d)", e.Key, code)
	}
	time.Sleep(2 * time.Second)
	gone := a.row(extra, e.Hash) == nil
	status := a.command("RefreshMonitoredDownloads")
	time.Sleep(2 * time.Second)
	back := a.row(extra, e.Hash) != nil
	after, ok := clientState(t, e.Hash)
	bl := a.blocklist()
	t.Logf("%s: row gone immediately=%v; after RefreshMonitoredDownloads (%s) row present=%v; blocklist %d → %d %v; history %v",
		e.Key, gone, status, back, blBefore, len(bl), bl, a.history(e.Hash))
	t.Logf("%s: client before %s/%s, after present=%v %s/%s; payload %s", e.Key, before.NativeState, before.VerifiedBytes,
		ok, after.NativeState, after.VerifiedBytes, digest)
	if !ok {
		t.Errorf("%s: torrent no longer registered in the client after removal with removeFromClient=false", e.Key)
	} else if after.NativeState != before.NativeState || after.VerifiedBytes != before.VerifiedBytes || after.Category != before.Category {
		t.Errorf("%s: client state changed: %s %v %v → %s %v %v", e.Key, before.NativeState, before.VerifiedBytes, before.Category,
			after.NativeState, after.VerifiedBytes, after.Category)
	}
	if d := payloadDigest(t, e); d != digest {
		t.Errorf("%s: payload changed: %s → %s", e.Key, digest, d)
	}
	if !gone {
		t.Errorf("%s: row still present right after accepted removal", e.Key)
	}
	// Observed on Sonarr 4.0.20.3014 and Radarr 6.4.4.10685 for rows of
	// unknown series/movies (no grab history): the request is accepted and
	// the row disappears, but it is tracked again after the next refresh and
	// nothing is blocklisted. Acceptance is therefore not a durable effect.
	// A version that behaves differently must be re-evaluated, not assumed.
	if !back {
		t.Errorf("%s: row did not return after refresh; released behaviour differs from the recorded semantics", e.Key)
	}
	if len(bl) != blBefore {
		t.Errorf("%s: blocklist changed (%d → %d) for an unknown-content row; re-evaluate the recorded semantics", e.Key, blBefore, len(bl))
	}
}

// Every flag is explicit: removeFromClient defaults to true in the released
// controller, which would delete the torrent.
func ignoreFlags() url.Values {
	return url.Values{"removeFromClient": {"false"}, "blocklist": {"false"}, "skipRedownload": {"true"}, "changeCategory": {"false"}}
}
func blocklistFlags() url.Values {
	return url.Values{"removeFromClient": {"false"}, "blocklist": {"true"}, "skipRedownload": {"true"}, "changeCategory": {"false"}}
}

func TestSonarrQueueRemovalSemantics(t *testing.T) {
	m := manifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	extra := url.Values{"includeUnknownSeriesItems": {"true"}}
	t.Run("ignore", func(t *testing.T) { a.t = t; probeRemoval(t, a, extra, m["half"], ignoreFlags()) })
	t.Run("blocklist", func(t *testing.T) { a.t = t; probeRemoval(t, a, extra, m["complete"], blocklistFlags()) })
}

func TestRadarrQueueRemovalSemantics(t *testing.T) {
	m := manifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	extra := url.Values{"includeUnknownMovieItems": {"true"}}
	t.Run("ignore", func(t *testing.T) { a.t = t; probeRemoval(t, a, extra, m["movie"], ignoreFlags()) })
	t.Run("blocklist", func(t *testing.T) { a.t = t; probeRemoval(t, a, extra, m["movie2"], blocklistFlags()) })
}
