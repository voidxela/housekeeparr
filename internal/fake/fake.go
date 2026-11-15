// Package fake provides deterministic, scriptable stand-ins for adapters
// and the clock. They are used by engine tests and by demo mode, so demo
// data flows through exactly the same pipeline as real observations.
package fake

import (
	"context"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Clock is a manually advanced clock with independent wall and monotonic
// readings, so tests can simulate wall-clock jumps.
type Clock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

// NewClock starts at t.
func NewClock(t time.Time) *Clock { return &Clock{wall: t} }

// Now returns the wall-clock reading.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

// MonoMS returns monotonic milliseconds since the clock was created.
func (c *Clock) MonoMS() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono.Milliseconds()
}

// Advance moves wall and monotonic time together.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
	c.mono += d
}

// JumpWall moves only the wall clock (e.g. an NTP step or manual change).
func (c *Clock) JumpWall(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
}

// Client is a scriptable download client.
type Client struct {
	mu        sync.Mutex
	id        string
	transfers []domain.Transfer
	err       error
	probeErr  error
	partial   bool
	probe     adapter.Probe
	lists     int
}

// NewClient creates a fake qBittorrent-kind client.
func NewClient(id string) *Client {
	return &Client{id: id, probe: adapter.Probe{App: "qBittorrent", Version: "v0.0.0-fake", Transport: "fake",
		Auth: "fake", Compatibility: adapter.CompatUntested, CompatNote: "synthetic adapter",
		Read: adapter.Capabilities{adapter.CapTransferList: {Support: adapter.Supported}}}}
}

func (c *Client) ID() string         { return c.id }
func (c *Client) Kind() adapter.Kind { return adapter.KindQBittorrent }

// SetProbe replaces the reported probe.
func (c *Client) SetProbe(p adapter.Probe) { c.mu.Lock(); c.probe = p; c.mu.Unlock() }

// Probe returns the scripted probe or error.
func (c *Client) Probe(context.Context) (adapter.Probe, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probeErr != nil {
		return adapter.Probe{}, c.probeErr
	}
	return c.probe, nil
}

// ListTransfers returns the scripted listing.
func (c *Client) ListTransfers(context.Context) (domain.TransferSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lists++
	if c.err != nil {
		return domain.TransferSnapshot{}, c.err
	}
	out := domain.TransferSnapshot{ClientID: c.id, CollectedAt: time.Now(), Complete: !c.partial}
	for _, t := range c.transfers {
		t.ClientID = c.id
		out.Transfers = append(out.Transfers, t)
	}
	if c.partial {
		out.Limitations = []string{"synthetic partial listing"}
	}
	return out, nil
}

// SetTransfers replaces the listing.
func (c *Client) SetTransfers(ts ...domain.Transfer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transfers = append([]domain.Transfer(nil), ts...)
}

// Update modifies one transfer in place by native ID.
func (c *Client) Update(id string, fn func(*domain.Transfer)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.transfers {
		if c.transfers[i].NativeID == id {
			fn(&c.transfers[i])
		}
	}
}

// Remove drops a transfer from the listing.
func (c *Client) Remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.transfers[:0]
	for _, t := range c.transfers {
		if t.NativeID != id {
			out = append(out, t)
		}
	}
	c.transfers = out
}

// Fail makes subsequent listings fail with err (nil restores success).
func (c *Client) Fail(err error) { c.mu.Lock(); c.err = err; c.mu.Unlock() }

// FailProbe makes probes fail.
func (c *Client) FailProbe(err error) { c.mu.Lock(); c.probeErr = err; c.mu.Unlock() }

// Partial marks listings as incomplete.
func (c *Client) Partial(p bool) { c.mu.Lock(); c.partial = p; c.mu.Unlock() }

// Lists returns how many listings were requested.
func (c *Client) Lists() int { c.mu.Lock(); defer c.mu.Unlock(); return c.lists }

// Manager is a scriptable media manager.
type Manager struct {
	mu        sync.Mutex
	id        string
	items     []domain.QueueItem
	health    []domain.HealthItem
	err       error
	healthErr error
	partial   bool
	reads     int
}

// NewManager creates a fake Sonarr-kind manager.
func NewManager(id string) *Manager { return &Manager{id: id} }

func (m *Manager) ID() string         { return m.id }
func (m *Manager) Kind() adapter.Kind { return adapter.KindSonarr }

// Probe always succeeds unless reads fail.
func (m *Manager) Probe(context.Context) (adapter.Probe, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return adapter.Probe{}, m.err
	}
	return adapter.Probe{App: "Sonarr", Version: "0.0.0-fake", Transport: "fake", Auth: "fake",
		Compatibility: adapter.CompatUntested, CompatNote: "synthetic adapter",
		Read: adapter.Capabilities{adapter.CapQueue: {Support: adapter.Supported}, adapter.CapHealth: {Support: adapter.Supported}}}, nil
}

// Queue returns the scripted queue.
func (m *Manager) Queue(context.Context) (domain.QueueSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.err != nil {
		return domain.QueueSnapshot{}, m.err
	}
	items := make([]domain.QueueItem, len(m.items))
	for i, q := range m.items {
		q.ManagerID = m.id
		items[i] = q
	}
	return domain.QueueSnapshot{ManagerID: m.id, CollectedAt: time.Now(), Items: items, TotalRecords: len(items), Complete: !m.partial}, nil
}

// Health returns the scripted health items.
func (m *Manager) Health(context.Context) (domain.HealthSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.healthErr != nil {
		return domain.HealthSnapshot{}, m.healthErr
	}
	return domain.HealthSnapshot{ManagerID: m.id, CollectedAt: time.Now(), Items: append([]domain.HealthItem(nil), m.health...)}, nil
}

// SetQueue replaces the queue.
func (m *Manager) SetQueue(items ...domain.QueueItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = append([]domain.QueueItem(nil), items...)
}

// SetHealth replaces health items.
func (m *Manager) SetHealth(items ...domain.HealthItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.health = append([]domain.HealthItem(nil), items...)
}

// Fail makes subsequent reads fail.
func (m *Manager) Fail(err error) { m.mu.Lock(); m.err = err; m.mu.Unlock() }

// Partial marks queue listings incomplete.
func (m *Manager) Partial(p bool) { m.mu.Lock(); m.partial = p; m.mu.Unlock() }

// Reads returns how many queue reads were requested.
func (m *Manager) Reads() int { m.mu.Lock(); defer m.mu.Unlock(); return m.reads }

// Transfer builds a synthetic transfer with every signal set explicitly.
func Transfer(id, name string, class domain.StateClass, verified, wanted int64, added time.Time) domain.Transfer {
	native := map[domain.StateClass]string{
		domain.StateDownloading: "stalledDL", domain.StatePaused: "stoppedDL", domain.StateQueued: "queuedDL",
		domain.StateChecking: "checkingDL", domain.StateMetadata: "metaDL", domain.StateComplete: "stalledUP",
		domain.StateError: "missingFiles", domain.StateTransient: "moving", domain.StateUnknown: "unknown",
	}[class]
	t := domain.Transfer{
		NativeID: id, Hashes: domain.Hashes{V1: id}, Name: name, NativeState: native, StateClass: class,
		Category: domain.KnownValue("tv-sonarr"), AddedAt: domain.KnownValue(added),
		WantedBytes: domain.KnownValue(wanted), VerifiedBytes: domain.KnownValue(verified),
		RemainingBytes: domain.KnownValue(wanted - verified), TotalBytes: domain.KnownValue(wanted),
		NetworkDownloadedBytes: domain.KnownValue(verified), DownloadRate: domain.KnownValue[int64](0),
		ConnectedSeeds: domain.KnownValue[int64](0), ConnectedPeers: domain.KnownValue[int64](0),
		SwarmSeeds: domain.UnknownValue[int64]("not reported"), SwarmPeers: domain.UnknownValue[int64]("not reported"),
		Availability: domain.UnknownValue[float64]("not computed"), AllFilesWanted: domain.KnownValue(true),
		Private: domain.KnownValue(false), ClientMessage: domain.UnsupportedValue[string]("synthetic"),
	}
	if class == domain.StateMetadata {
		t.WantedBytes = domain.UnknownValue[int64]("no metadata")
		t.VerifiedBytes = domain.UnknownValue[int64]("no metadata")
		t.AllFilesWanted = domain.UnknownValue[bool]("no metadata")
		t.Private = domain.UnknownValue[bool]("privacy unknown until metadata is acquired")
	}
	return t
}

// Episode builds a synthetic Sonarr queue row for a transfer hash.
func Episode(queueID int64, downloadID, title, series string, season, episode int64) domain.QueueItem {
	return domain.QueueItem{
		QueueID: queueID, DownloadID: downloadID, Protocol: "torrent", DownloadClient: "qBittorrent", Title: title,
		ContentKind: "episode", SeriesID: 1, SeriesTitle: series, EpisodeID: season*100 + episode,
		SeasonNumber: domain.KnownValue(season), EpisodeNumber: domain.KnownValue(episode),
		Status: "downloading", TrackedState: domain.KnownValue("downloading"), TrackedStatus: domain.KnownValue("ok"),
		SizeBytes: domain.KnownValue[int64](1 << 30), RemainingBytes: domain.KnownValue[int64](1 << 29),
		Added: domain.UnknownValue[time.Time]("synthetic"),
	}
}
