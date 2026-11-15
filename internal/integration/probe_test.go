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

	"github.com/voidxela/housekeeparr/internal/domain"
)

type arrAPI struct {
	t        *testing.T
	base     string
	key      string
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
			EventType string `json:"eventType"`
		} `json:"records"`
	}
	_ = json.Unmarshal(b, &pg)
	var out []string
	for _, r := range pg.Records {
		out = append(out, r.EventType)
	}
	return out
}

// payloadDigest hashes every payload file of a torrent in the qBittorrent
// save directory; a missing file is recorded as such.
func payloadDigest(t *testing.T, e entry) string {
	t.Helper()
	dir := filepath.Join(env(t, "HK_IT_WORK"), "qbt-downloads")
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
	snap, err := qbit(t).ListTransfers(context.Background())
	if err != nil || !snap.Complete {
		t.Fatalf("qBittorrent listing: %v", err)
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
