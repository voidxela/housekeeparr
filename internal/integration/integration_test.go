//go:build integration

// Package integration validates adapters against disposable released
// instances started by scripts/it/run.sh. It never runs in the default test
// suite. Observed values are logged so a run documents real behaviour.
package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/qbittorrent"
	"github.com/voidxela/housekeeparr/internal/adapter/sonarr"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/store"
)

const fakePasskey = "0123456789abcdef0123456789abcdef"

type entry struct {
	Key, Name, Hash, Expect string
	Private                 bool
	Deselect                string
	Manager                 string
	Files                   []string
}

func env(t *testing.T, k string) string {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s not set; run scripts/it/run.sh", k)
	}
	return v
}

func manifest(t *testing.T) map[string]entry {
	b, err := os.ReadFile(env(t, "HK_IT_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	var list []entry
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]entry{}
	for _, e := range list {
		out[e.Key] = e
	}
	return out
}

func qbit(t *testing.T) *qbittorrent.Client {
	c, err := qbittorrent.New(qbittorrent.Options{ID: "qbit", BaseURL: env(t, "HK_IT_QBT_URL"), Username: env(t, "HK_IT_QBT_USER"),
		Password: env(t, "HK_IT_QBT_PASS"), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestQBittorrentReleasedBehaviour(t *testing.T) {
	ctx := context.Background()
	m := manifest(t)
	c := qbit(t)
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("probe: %s %s webapi %s, compatibility %s", p.App, p.Version, p.APIVersion, p.Compatibility)
	if want := "v" + env(t, "HK_IT_QBT_VERSION"); p.Version != want {
		t.Fatalf("version %s, want %s", p.Version, want)
	}
	byHash := map[string]domain.Transfer{}
	deadline := time.Now().Add(90 * time.Second)
	for {
		snap, err := c.ListTransfers(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		byHash = map[string]domain.Transfer{}
		settled := true
		for _, tr := range snap.Transfers {
			byHash[tr.NativeID] = tr
			if tr.StateClass == domain.StateChecking || tr.StateClass == domain.StateTransient {
				settled = false
			}
		}
		if len(byHash) >= len(m) && settled || time.Now().After(deadline) {
			if !snap.Complete {
				t.Fatalf("real torrent listing reported incomplete: %v", snap.Limitations)
			}
			b, _ := json.Marshal(snap)
			if strings.Contains(string(b), fakePasskey) || strings.Contains(string(b), "127.0.0.1:9") {
				t.Fatal("normalized snapshot contains tracker data")
			}
			break
		}
		time.Sleep(2 * time.Second)
	}
	five := strings.HasPrefix(env(t, "HK_IT_QBT_VERSION"), "5.")
	for key, e := range m {
		tr, ok := byHash[e.Hash]
		if !ok {
			t.Errorf("%s: torrent %s not listed", key, e.Hash)
			continue
		}
		t.Logf("%-10s native=%-12s class=%-11s verified=%v wanted=%v avail=%v(%s) private=%v allWanted=%v seeds=%v swarm=%v",
			key, tr.NativeState, tr.StateClass, tr.VerifiedBytes, tr.WantedBytes, tr.Availability, tr.Availability.Status,
			tr.Private, tr.AllFilesWanted, tr.ConnectedSeeds, tr.SwarmSeeds)
		switch key {
		case "complete":
			if tr.StateClass != domain.StateComplete || tr.VerifiedBytes != tr.WantedBytes {
				t.Errorf("complete: %s verified %v wanted %v", tr.StateClass, tr.VerifiedBytes, tr.WantedBytes)
			}
		case "half":
			if tr.StateClass != domain.StateDownloading {
				t.Errorf("half: class %s", tr.StateClass)
			}
			v, _ := tr.VerifiedBytes.Get()
			w, _ := tr.WantedBytes.Get()
			if v <= 0 || v >= w {
				t.Errorf("half: verified %d of %d after recheck", v, w)
			}
			// Validates AvailabilityMeaning: with no peers, libtorrent's
			// distributed copies should count this client's own pieces.
			if a, ok := tr.Availability.Get(); !ok || a <= 0 || a >= 1 {
				t.Errorf("half: availability %v (%s); the documented meaning (includes own pieces) is not confirmed", tr.Availability.Value, tr.Availability.Status)
			}
		case "empty":
			if tr.StateClass != domain.StateDownloading || tr.VerifiedBytes.Value != 0 {
				t.Errorf("empty: %s %v", tr.StateClass, tr.VerifiedBytes)
			}
		case "private":
			if five && (!tr.Private.IsKnown() || !tr.Private.Value) {
				t.Errorf("private: %v", tr.Private)
			}
			if !five && tr.Private.Status != domain.Unsupported {
				t.Errorf("private on 4.x must be unsupported: %v", tr.Private)
			}
		case "deselected":
			if all, ok := tr.AllFilesWanted.Get(); !ok || all {
				t.Errorf("deselected: AllFilesWanted %v", tr.AllFilesWanted)
			}
		case "stopped":
			if tr.StateClass != domain.StatePaused {
				t.Errorf("stopped: %s (%s)", tr.StateClass, tr.NativeState)
			}
			if tr.Availability.IsKnown() {
				t.Errorf("stopped: availability must be unknown, got %v", tr.Availability)
			}
		case "errored":
			// Payload deleted after verification. libtorrent does not notice
			// until it reads the data, so qBittorrent and Deluge still report
			// the torrent complete: "complete" is not evidence that data exists.
			if tr.StateClass != domain.StateComplete {
				t.Errorf("errored: %s (%s); released behaviour differs from the recorded semantics", tr.StateClass, tr.NativeState)
			}
		case "errored-restart":
			// Payload deleted while qBittorrent was stopped cleanly, then restarted.
			// Observed on 5.3.0 and 4.6.7: missingFiles -> StateError, verified=0.
			if tr.StateClass != domain.StateError {
				t.Errorf("errored-restart: got state %s (%s), want error", tr.StateClass, tr.NativeState)
			}
		case "errored-recheck":
			// Payload deleted while running, then forced recheck (/api/v2/torrents/recheck).
			// Observed on 5.3.0 and 4.6.7: stalledDL -> StateDownloading with verified=0
			// (no error exposed); do not assert StateError.
			if tr.StateClass != domain.StateDownloading || !tr.VerifiedBytes.IsKnown() || tr.VerifiedBytes.Value != 0 {
				t.Errorf("errored-recheck: got state %s (%s) verified=%v, want downloading with verified=0",
					tr.StateClass, tr.NativeState, tr.VerifiedBytes)
			}
		case "magnet":
			if tr.StateClass != domain.StateMetadata || tr.WantedBytes.IsKnown() || tr.Availability.IsKnown() {
				t.Errorf("magnet: %s wanted %v availability %v", tr.StateClass, tr.WantedBytes, tr.Availability)
			}
		}
	}
	if dir := os.Getenv("HK_IT_CAPTURE_DIR"); dir != "" {
		captureQBit(t, dir)
	}
}

// captureQBit writes a sanitized raw torrents/info response for review.
func captureQBit(t *testing.T, dir string) {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	base := env(t, "HK_IT_QBT_URL")
	resp, err := hc.PostForm(base+"/api/v2/auth/login", url.Values{"username": {env(t, "HK_IT_QBT_USER")}, "password": {env(t, "HK_IT_QBT_PASS")}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = hc.Get(base + "/api/v2/torrents/info")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	work := env(t, "HK_IT_WORK")
	for _, r := range rows {
		for _, k := range []string{"tracker", "magnet_uri", "save_path", "content_path", "download_path", "root_path", "comment"} {
			if _, ok := r[k]; ok {
				r[k] = "[sanitized]"
			}
		}
	}
	out, _ := json.MarshalIndent(rows, "", " ")
	s := strings.ReplaceAll(strings.ReplaceAll(string(out), fakePasskey, "[passkey]"), work, "[work]")
	name := filepath.Join(dir, "qbittorrent-v"+env(t, "HK_IT_QBT_VERSION")+"-torrents_info.json")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(name, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured %s (review before committing)", name)
}

func sonarrClient(t *testing.T) *sonarr.Client {
	c, err := sonarr.New(sonarr.Options{ID: "sonarr", BaseURL: env(t, "HK_IT_SONARR_URL"), APIKey: env(t, "HK_IT_SONARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSonarrReleasedBehaviour(t *testing.T) {
	ctx := context.Background()
	m := manifest(t)
	c := sonarrClient(t)
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("probe: %s %s, compatibility %s", p.App, p.Version, p.Compatibility)
	if p.Version != env(t, "HK_IT_SONARR_VERSION") {
		t.Fatalf("version %s", p.Version)
	}
	h, err := c.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	for _, it := range h.Items {
		t.Logf("health: %s %s %q", it.Type, it.Source, it.Message)
	}
	hashes := map[string]string{}
	for k, e := range m {
		hashes[e.Hash] = k
	}
	var q domain.QueueSnapshot
	deadline := time.Now().Add(120 * time.Second)
	for {
		q, err = c.Queue(ctx)
		if err != nil {
			t.Fatalf("queue: %v", err)
		}
		found := 0
		for _, it := range q.Items {
			if hashes[it.DownloadID] != "" {
				found++
			}
		}
		if found > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !q.Complete || q.TotalRecords != len(q.Items) {
		t.Errorf("real queue listing incomplete or inconsistent: complete=%v total=%d rows=%d %v", q.Complete, q.TotalRecords, len(q.Items), q.Limitations)
	}
	matched := 0
	for _, it := range q.Items {
		t.Logf("queue: id=%d dl=%s key=%s state=%v status=%v client=%q title=%q messages=%v",
			it.QueueID, it.DownloadID, hashes[it.DownloadID], it.TrackedState, it.TrackedStatus, it.DownloadClient, it.Title, it.StatusMessages)
		if hashes[it.DownloadID] != "" {
			matched++
		}
	}
	if matched == 0 {
		t.Errorf("no queue row carries a manifest infohash as its download id (%d rows); Sonarr's unknown-series tracking or download id format differs from the adapter's assumption", len(q.Items))
	}
}

func TestEngineCorrelatesRealInstances(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dcMap, clientBlock := managerTOMLConfig(t)
	cfgSrc := `version = 1
[polling]
jitter = "0s"
[[sonarr]]
id = "sonarr"
url = "` + env(t, "HK_IT_SONARR_URL") + `"
api_key = "env:HK_IT_SONARR_KEY"
` + dcMap + "\n" + clientBlock
	cfg, _, err := config.Parse([]byte(cfgSrc), filepath.Join(dir, "h.toml"))
	if err != nil {
		t.Fatal(err)
	}
	secrets, probs := cfg.ResolvedSecrets(nil)
	for _, p := range probs {
		if p.Severity == "error" {
			t.Fatal(p)
		}
	}
	conns, err := engine.Build(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range engine.Check(ctx, conns, 15*time.Second, 2) {
		if !r.OK {
			t.Fatalf("check %s: %s %s", r.ID, r.ErrorClass, r.Error)
		}
	}
	st, err := store.Open(ctx, dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e, err := engine.New(ctx, engine.Options{Config: cfg, Store: st, Conns: conns, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "once", Version: "it"})
	if err != nil {
		t.Fatal(err)
	}
	rep := e.Cycle(ctx, nil)
	for _, c := range rep.Connections {
		if !c.OK {
			t.Fatalf("cycle %s: %s", c.ID, c.Error)
		}
	}
	items, _ := st.QueueItems(ctx, store.QueueFilter{})
	matched := 0
	for _, it := range items {
		t.Logf("correlation: %s %s %s", it.Latest.Title, it.MatchStatus, it.MatchNote)
		if it.MatchStatus == store.MatchMatched {
			matched++
		}
	}
	if matched == 0 {
		t.Error("no queue row correlated with a qBittorrent transfer")
	}
	_ = adapter.CompatTested
}
