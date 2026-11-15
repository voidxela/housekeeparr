// Package demo generates clearly-labelled synthetic state by running the
// real engine over scripted fake adapters and a simulated clock. Demo state
// is stored with data origin "demo" and can never be opened as live state.
package demo

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Config is the synthetic configuration used by demo mode.
const Config = `
version = 1
mode = "observe"
[state]
dir = "."
[web]
enabled = true
listen = "127.0.0.1:8485"
[polling]
interval = "10m"
jitter = "0s"
[[sonarr]]
id = "demo-sonarr"
name = "Sonarr (demo)"
url = "https://sonarr.demo.invalid"
api_key = "env:HOUSEKEEPARR_DEMO_UNUSED"
download_clients = { "qBittorrent" = "demo-qbit" }
[[qbittorrent]]
id = "demo-qbit"
name = "qBittorrent (demo)"
url = "https://qbittorrent.demo.invalid"
[[qbittorrent]]
id = "demo-seedbox"
name = "Seedbox qBittorrent (demo)"
url = "https://seedbox.demo.invalid"
`

const gib = int64(1) << 30

// Hashes are obviously synthetic.
const (
	hStalled  = "de00000000000000000000000000000000000001"
	hSlow     = "de00000000000000000000000000000000000002"
	hNew      = "de00000000000000000000000000000000000003"
	hPaused   = "de00000000000000000000000000000000000004"
	hMagnet   = "de00000000000000000000000000000000000005"
	hPack     = "de00000000000000000000000000000000000006"
	hMissing  = "de00000000000000000000000000000000000007"
	hNearDone = "de00000000000000000000000000000000000008"
	hSeeding  = "de00000000000000000000000000000000000009"
)

// Demo is a generated demo environment.
type Demo struct {
	Config *config.Config
	Store  *store.Store
	Engine *engine.Engine
	Clock  *fake.Clock
	client *fake.Client
}

// Generate builds about four hours of synthetic history ending at end.
func Generate(ctx context.Context, dir string, end time.Time) (*Demo, error) {
	cfg, _, err := config.Parse([]byte(Config), dir+"/housekeeparr.toml")
	if err != nil {
		return nil, err
	}
	cfg.State.Dir = dir
	start := end.Add(-4 * time.Hour)
	clock := fake.NewClock(start)
	st, err := store.Open(ctx, dir, store.Options{Origin: store.OriginDemo, Now: clock.Now})
	if err != nil {
		return nil, err
	}
	qbit, seedbox, sonarr := fake.NewClient("demo-qbit"), fake.NewClient("demo-seedbox"), fake.NewManager("demo-sonarr")
	for _, c := range []*fake.Client{qbit, seedbox} {
		p, _ := c.Probe(ctx)
		p.App, p.Version, p.CompatNote = "qBittorrent (synthetic)", "demo", "synthetic demo adapter; not a real application"
		c.SetProbe(p)
	}
	meta := map[string]engine.ConnMeta{
		"demo-sonarr":  {ID: "demo-sonarr", Name: "Sonarr (demo)", BaseURL: "https://sonarr.demo.invalid", Kind: adapter.KindSonarr, Enabled: true, SecretRef: "env:HOUSEKEEPARR_DEMO_UNUSED", DownloadClients: cfg.Sonarr[0].DownloadClients},
		"demo-qbit":    {ID: "demo-qbit", Name: "qBittorrent (demo)", BaseURL: "https://qbittorrent.demo.invalid", Kind: adapter.KindQBittorrent, Enabled: true},
		"demo-seedbox": {ID: "demo-seedbox", Name: "Seedbox qBittorrent (demo)", BaseURL: "https://seedbox.demo.invalid", Kind: adapter.KindQBittorrent, Enabled: true},
	}
	conns := &engine.Connections{Clients: []adapter.DownloadClient{qbit, seedbox}, Managers: []adapter.MediaManager{sonarr}, Meta: meta}
	eng, err := engine.New(ctx, engine.Options{Config: cfg, Store: st, Conns: conns, Clock: clock,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "demo", Version: "demo"})
	if err != nil {
		st.Close()
		return nil, err
	}
	d := &Demo{Config: cfg, Store: st, Engine: eng, Clock: clock, client: qbit}

	tr := func(id, name string, class domain.StateClass, v, w int64, added time.Time) domain.Transfer {
		return fake.Transfer(id, name, class, v, w, added)
	}
	stalled := tr(hStalled, "Harbor.Lights.S02E05.1080p.WEB.h264-DEMO", domain.StateDownloading, 610<<20, 2*gib, start.Add(-20*time.Minute))
	stalled.SwarmSeeds = domain.KnownValue[int64](2)
	slow := tr(hSlow, "Quiet.Valley.S01E03.720p.HDTV.x264-DEMO", domain.StateDownloading, 300<<20, 1200<<20, start.Add(-10*time.Minute))
	slow.ConnectedSeeds, slow.DownloadRate = domain.KnownValue[int64](1), domain.KnownValue[int64](22_000)
	paused := tr(hPaused, "Northern.Line.S04E01.1080p.WEB.h264-DEMO", domain.StateDownloading, 900<<20, 3*gib, start.Add(-30*time.Minute))
	magnet := tr(hMagnet, "Old.Archive.S01E01.480p.DVDRip-DEMO", domain.StateMetadata, 0, 0, start.Add(-5*time.Minute))
	pack := tr(hPack, "Harbor.Lights.S01.1080p.WEB.h264-DEMO", domain.StateComplete, 14*gib, 14*gib, start.Add(-6*time.Hour))
	missing := tr(hMissing, "Quiet.Valley.S01E01.720p.HDTV.x264-DEMO", domain.StateError, gib/2, gib, start.Add(-3*time.Hour))
	nearDone := tr(hNearDone, "Long.Road.S03E09.2160p.WEB.h265-DEMO", domain.StateDownloading, 7*gib*97/100, 7*gib, start.Add(-40*time.Minute))
	nearDone.Private = domain.KnownValue(true)
	nearDone.Availability = domain.Signal[float64]{Status: domain.Known, Value: 0.97, Note: "synthetic"}
	seeding := tr(hSeeding, "Northern.Line.S03E10.1080p.WEB.h264-DEMO", domain.StateComplete, 2*gib, 2*gib, start.Add(-48*time.Hour))
	qbit.SetTransfers(stalled, slow, paused, magnet, pack, missing, nearDone, seeding)

	ep := func(qid int64, dl, title, series string, s, e int64) domain.QueueItem {
		q := fake.Episode(qid, dl, title, series, s, e)
		q.SeriesID = int64(len(series))
		return q
	}
	blocked := func(q domain.QueueItem) domain.QueueItem {
		q.Status = "completed"
		q.TrackedState, q.TrackedStatus = domain.KnownValue("importBlocked"), domain.KnownValue("warning")
		q.StatusMessages = []domain.StatusMessage{{Title: q.Title, Messages: []string{"One or more episodes expected in this release were not imported or missing from the release"}}}
		return q
	}
	usenet := ep(9, "SABnzbd_nzo_demo", "Quiet.Valley.S01E04.720p.WEB-DEMO", "Quiet Valley", 1, 4)
	usenet.Protocol, usenet.DownloadClient = "usenet", "SABnzbd"
	sonarr.SetQueue(
		ep(1, hStalled, stalled.Name, "Harbor Lights", 2, 5),
		ep(2, hSlow, slow.Name, "Quiet Valley", 1, 3),
		ep(3, hPaused, paused.Name, "Northern Line", 4, 1),
		ep(4, hMagnet, magnet.Name, "Old Archive", 1, 1),
		blocked(ep(5, hPack, pack.Name, "Harbor Lights", 1, 9)),
		blocked(ep(6, hPack, pack.Name, "Harbor Lights", 1, 10)),
		ep(7, hMissing, missing.Name, "Quiet Valley", 1, 1),
		ep(8, hNearDone, nearDone.Name, "Long Road", 3, 9),
		usenet,
	)
	sonarr.SetHealth(domain.HealthItem{Source: "IndexerSearchCheck", Type: "warning",
		Message: "All search-capable indexers are temporarily unavailable due to recent indexer errors",
		WikiURL: "https://wiki.servarr.com/sonarr/system#indexers-are-unavailable-due-to-failures"})
	seedbox.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "demo", Message: "dial tcp: connection refused (synthetic)"})

	steps := int(end.Sub(start) / (10 * time.Minute))
	for i := 0; i <= steps; i++ {
		elapsed := time.Duration(i) * 10 * time.Minute
		// Slow torrent: one 4 MiB piece every 20 minutes.
		if i%2 == 0 {
			qbit.Update(hSlow, func(t *domain.Transfer) { t.VerifiedBytes = domain.KnownValue(t.VerifiedBytes.Value + 4<<20) })
		}
		// A new download appears 15 minutes before the end.
		if elapsed == end.Sub(start)-20*time.Minute {
			qbit.SetTransfers(append(currentTransfers(ctx, qbit), tr(hNew, "Bright.Coast.S01E01.1080p.WEB.h264-DEMO", domain.StateDownloading, 0, 1500<<20, clock.Now()))...)
			sonarr.SetQueue(append(currentQueue(ctx, sonarr), ep(10, hNew, "Bright.Coast.S01E01.1080p.WEB.h264-DEMO", "Bright Coast", 1, 1))...)
		}
		// The no-progress torrent is diagnosed, then a user pauses it.
		if elapsed == 3*time.Hour {
			qbit.Update(hPaused, func(t *domain.Transfer) { t.StateClass, t.NativeState = domain.StatePaused, "stoppedDL" })
		}
		if i > 0 {
			clock.Advance(10 * time.Minute)
		}
		if rep := eng.Cycle(ctx, nil); rep.StoreError != "" {
			st.Close()
			return nil, errString(rep.StoreError)
		}
	}
	return d, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func currentTransfers(ctx context.Context, c *fake.Client) []domain.Transfer {
	s, _ := c.ListTransfers(ctx)
	return s.Transfers
}

func currentQueue(ctx context.Context, m *fake.Manager) []domain.QueueItem {
	q, _ := m.Queue(ctx)
	return q.Items
}

// Tick keeps the demo worker alive: it advances the simulated clock to the
// real time, records a heartbeat, and runs a cycle when one is due.
func (d *Demo) Tick(ctx context.Context, now time.Time, lastCycle *time.Time) {
	if gap := now.Sub(d.Clock.Now()); gap > 0 {
		d.Clock.Advance(gap)
	}
	_ = d.Engine.Heartbeat(ctx)
	if now.Sub(*lastCycle) >= d.Config.Polling.Interval.Duration {
		d.client.Update(hSlow, func(t *domain.Transfer) { t.VerifiedBytes = domain.KnownValue(t.VerifiedBytes.Value + 4<<20) })
		d.Engine.Cycle(ctx, nil)
		*lastCycle = now
	}
}
