package engine

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/store"
)

// TestReferenceWorkload measures one simulated day (144 ten-minute cycles)
// of a larger multi-instance installation: two clients with 2,000
// transfers each (100 incomplete per client, 60 of them stalled) and two
// managers with 150 queue rows each. It reports cycle time, state size, and
// heap; these are measurements on the development host, not release budgets.
// Run with: go test ./internal/engine -run ReferenceWorkload -v
func TestReferenceWorkload(t *testing.T) {
	if testing.Short() || os.Getenv("HK_WORKLOAD") == "" {
		t.Skip("set HK_WORKLOAD=1 to run the reference workload")
	}
	cfgSrc := `version = 1
[polling]
jitter = "0s"
[[sonarr]]
id = "sonarr"
url = "http://127.0.0.1:8989"
api_key = "env:X"
download_clients = { "qBittorrent" = "qbit-a" }
[[radarr]]
id = "radarr"
url = "http://127.0.0.1:7878"
api_key = "env:Y"
download_clients = { "qBittorrent" = "qbit-b" }
[[qbittorrent]]
id = "qbit-a"
url = "http://127.0.0.1:8080"
[[qbittorrent]]
id = "qbit-b"
url = "http://127.0.0.1:8081"
`
	cfg, _, err := config.Parse([]byte(cfgSrc), "/x/h.toml")
	if err != nil {
		t.Fatal(err)
	}
	clock := fake.NewClock(t0)
	dir := t.TempDir()
	st, err := store.Open(ctx, dir, store.Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	clients := []*fake.Client{fake.NewClient("qbit-a"), fake.NewClient("qbit-b")}
	mgrs := []*fake.Manager{fake.NewManager("sonarr"), fake.NewRadarr("radarr")}
	for ci, c := range clients {
		var ts []domain.Transfer
		var queue []domain.QueueItem
		for i := 0; i < 2000; i++ {
			h := fmt.Sprintf("%02x%038x", ci, i)
			class := domain.StateComplete
			verified := 2 * gib
			if i < 100 {
				class, verified = domain.StateDownloading, gib
			}
			ts = append(ts, fake.Transfer(h, fmt.Sprintf("Workload.%d.%d", ci, i), class, verified, 2*gib, t0.Add(-time.Hour)))
			if i < 150 {
				if ci == 0 {
					queue = append(queue, fake.Episode(int64(i+1), h, "W", "Show", 1, int64(i+1)))
				} else {
					queue = append(queue, fake.Movie(int64(i+1), h, "W", "Film", 2020))
				}
			}
		}
		c.SetTransfers(ts...)
		mgrs[ci].SetQueue(queue...)
	}
	meta := map[string]ConnMeta{
		"sonarr": {ID: "sonarr", Name: "Sonarr", Kind: adapter.KindSonarr, Enabled: true, DownloadClients: cfg.Sonarr[0].DownloadClients},
		"radarr": {ID: "radarr", Name: "Radarr", Kind: adapter.KindRadarr, Enabled: true, DownloadClients: cfg.Radarr[0].DownloadClients},
		"qbit-a": {ID: "qbit-a", Name: "A", Kind: adapter.KindQBittorrent, Enabled: true},
		"qbit-b": {ID: "qbit-b", Name: "B", Kind: adapter.KindQBittorrent, Enabled: true},
	}
	conns := &Connections{Clients: []adapter.DownloadClient{clients[0], clients[1]}, Managers: []adapter.MediaManager{mgrs[0], mgrs[1]}, Meta: meta}
	e, err := New(ctx, Options{Config: cfg, Store: st, Conns: conns, Clock: clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	var total, worst time.Duration
	stmts0 := st.Statements()
	cycles := 144
	if n, err := strconv.Atoi(os.Getenv("HK_WORKLOAD_CYCLES")); err == nil && n > 0 {
		cycles = n
	}
	for i := 0; i < cycles; i++ {
		// 40 of each client's 100 incomplete transfers make slow progress.
		for ci, c := range clients {
			for j := 60; j < 100; j++ {
				c.Update(fmt.Sprintf("%02x%038x", ci, j), func(t *domain.Transfer) { t.VerifiedBytes = domain.KnownValue(t.VerifiedBytes.Value + 4<<20) })
			}
		}
		start := time.Now()
		rep := e.Cycle(ctx, nil)
		d := time.Since(start)
		if rep.StoreError != "" {
			t.Fatal(rep.StoreError)
		}
		total += d
		if d > worst {
			worst = d
		}
		clock.Advance(10 * time.Minute)
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	var dbBytes int64
	for _, f := range []string{"housekeeparr.db", "housekeeparr.db-wal"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err == nil {
			dbBytes += fi.Size()
		}
	}
	fs, _ := st.Findings(ctx, store.FindingFilter{States: []domain.FindingState{domain.FindingOpen}})
	t.Logf("reference workload: 4,000 transfers (200 incomplete), 300 queue rows, %d cycles", cycles)
	t.Logf("cycle time: mean %s, worst %s (fake adapters; excludes network)", (total / time.Duration(cycles)).Round(time.Millisecond), worst.Round(time.Millisecond))
	t.Logf("state after one simulated day: %.1f MiB (db + wal)", float64(dbBytes)/(1<<20))
	t.Logf("write statements: %.0f per cycle", float64(st.Statements()-stmts0)/float64(cycles))
	t.Logf("heap after GC: %.1f MiB in use, %.1f MiB obtained from OS", float64(ms.HeapInuse)/(1<<20), float64(ms.Sys)/(1<<20))
	t.Logf("open findings: %d (expected 120 stalls)", len(fs))
	if len(fs) != 120 {
		t.Errorf("expected 120 stall findings, got %d", len(fs))
	}
}
