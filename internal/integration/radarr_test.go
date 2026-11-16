//go:build integration

package integration

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/store"
)

func TestRadarrReleasedBehaviour(t *testing.T) {
	ctx := context.Background()
	m := manifest(t)
	c, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("probe: %s %s, compatibility %s", p.App, p.Version, p.Compatibility)
	if p.Version != env(t, "HK_IT_RADARR_VERSION") {
		t.Fatalf("version %s", p.Version)
	}
	bad, _ := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: "0000", Timeout: 15 * time.Second})
	if _, err := bad.Probe(ctx); err == nil {
		t.Error("a wrong API key must fail")
	}
	h, err := c.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	for _, it := range h.Items {
		t.Logf("health: %s %s %q", it.Type, it.Source, it.Message)
	}
	movie := m["movie"]
	var q domain.QueueSnapshot
	deadline := time.Now().Add(120 * time.Second)
	for {
		q, err = c.Queue(ctx)
		if err != nil {
			t.Fatalf("queue: %v", err)
		}
		found := false
		for _, it := range q.Items {
			found = found || it.DownloadID == movie.Hash
		}
		if found || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if !q.Complete || q.TotalRecords != len(q.Items) {
		t.Errorf("real queue listing incomplete or inconsistent: complete=%v total=%d rows=%d %v", q.Complete, q.TotalRecords, len(q.Items), q.Limitations)
	}
	matched := 0
	for _, it := range q.Items {
		t.Logf("queue: id=%d dl=%s state=%v status=%v client=%q kind=%s title=%q messages=%v",
			it.QueueID, it.DownloadID, it.TrackedState, it.TrackedStatus, it.DownloadClient, it.ContentKind, it.Title, it.StatusMessages)
		if it.DownloadID == movie.Hash {
			matched++
			if it.DownloadClient != expectedManagerClientName() || it.Protocol != "torrent" {
				t.Errorf("movie row: client %q (want %q) protocol %q", it.DownloadClient, expectedManagerClientName(), it.Protocol)
			}
		}
		for k, e := range m {
			if e.Manager != "radarr" && it.DownloadID == e.Hash {
				if managerClientKind() == "deluge" && os.Getenv("HK_IT_DELUGE_LABEL") == "" {
					t.Logf("Radarr lists %s (expected when Deluge Label plugin is unavailable and category is empty)", k)
				} else {
					t.Errorf("Radarr lists %s from the Sonarr category", k)
				}
			}
		}
	}
	if matched != 1 {
		t.Errorf("movie torrent appears %d times in Radarr's queue (want 1); unknown-movie tracking or download id format differs from the adapter's assumption", matched)
	}
}

func TestEngineCorrelatesRadarr(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dcMap, clientBlock := managerTOMLConfig(t)
	cfgSrc := `version = 1
[polling]
jitter = "0s"
[[radarr]]
id = "radarr"
url = "` + env(t, "HK_IT_RADARR_URL") + `"
api_key = "env:HK_IT_RADARR_KEY"
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
	movie := manifest(t)["movie"]
	items, _ := st.QueueItems(ctx, store.QueueFilter{ManagerID: "radarr"})
	ok := false
	for _, it := range items {
		t.Logf("correlation: %s %s %s", it.Latest.Title, it.MatchStatus, it.MatchNote)
		if it.DownloadID == movie.Hash && it.MatchStatus == store.MatchMatched && it.MatchNativeID == movie.Hash {
			ok = true
		}
	}
	if !ok {
		t.Error("Radarr's movie row did not correlate with the qBittorrent transfer")
	}
}
