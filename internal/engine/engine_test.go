package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/store"
)

var ctx = context.Background()

const (
	gib  = int64(1) << 30
	hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

const baseConfig = `
version = 1
[polling]
interval = "10m"
jitter = "0s"
[[sonarr]]
id = "sonarr"
name = "Sonarr"
url = "http://127.0.0.1:8989"
api_key = "env:X"
download_clients = { "qBittorrent" = "qbit" }
[[qbittorrent]]
id = "qbit"
name = "qBittorrent"
url = "http://127.0.0.1:8080"
[[qbittorrent]]
id = "qbit2"
name = "qBittorrent 2"
url = "http://127.0.0.1:8081"
`

type harness struct {
	t      *testing.T
	dir    string
	cfg    *config.Config
	clock  *fake.Clock
	client *fake.Client
	other  *fake.Client
	mgr    *fake.Manager
	st     *store.Store
	eng    *Engine
	// Optional real adapters replacing client "qbit" and manager "sonarr".
	realClient adapter.DownloadClient
	realMgr    adapter.MediaManager
}

func newHarness(t *testing.T, cfgSrc string) *harness {
	t.Helper()
	return newHarnessWith(t, cfgSrc, nil)
}

// newHarnessWith lets a test install real adapters before the engine starts.
func newHarnessWith(t *testing.T, cfgSrc string, setup func(*harness)) *harness {
	t.Helper()
	cfg, _, err := config.Parse([]byte(cfgSrc), "/x/housekeeparr.toml")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: t.TempDir(), cfg: cfg, clock: fake.NewClock(t0),
		client: fake.NewClient("qbit"), other: fake.NewClient("qbit2"), mgr: fake.NewManager("sonarr")}
	if setup != nil {
		setup(h)
	}
	h.start()
	return h
}

func (h *harness) conns() *Connections {
	meta := map[string]ConnMeta{
		"sonarr": {ID: "sonarr", Name: "Sonarr", BaseURL: "http://127.0.0.1:8989", Kind: adapter.KindSonarr, Enabled: true,
			SecretRef: "env:X", DownloadClients: h.cfg.Sonarr[0].DownloadClients},
		"qbit":  {ID: "qbit", Name: "qBittorrent", BaseURL: "http://127.0.0.1:8080", Kind: adapter.KindQBittorrent, Enabled: true},
		"qbit2": {ID: "qbit2", Name: "qBittorrent 2", BaseURL: "http://127.0.0.1:8081", Kind: adapter.KindQBittorrent, Enabled: true},
	}
	var client adapter.DownloadClient = h.client
	var mgr adapter.MediaManager = h.mgr
	if h.realClient != nil {
		client = h.realClient
		m := meta["qbit"]
		m.Kind = client.Kind()
		meta["qbit"] = m
	}
	if h.realMgr != nil {
		mgr = h.realMgr
	}
	return &Connections{Clients: []adapter.DownloadClient{client, h.other}, Managers: []adapter.MediaManager{mgr}, Meta: meta}
}

func (h *harness) start() {
	h.t.Helper()
	st, err := store.Open(ctx, h.dir, store.Options{Now: h.clock.Now})
	if err != nil {
		h.t.Fatal(err)
	}
	h.st = st
	e, err := New(ctx, Options{Config: h.cfg, Store: st, Conns: h.conns(), Clock: h.clock,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "test", Version: "test"})
	if err != nil {
		h.t.Fatal(err)
	}
	h.eng = e
	h.t.Cleanup(func() { h.st.Close() })
}

// restart simulates a process restart (optionally without clean shutdown).
func (h *harness) restart(clean bool) {
	h.t.Helper()
	if clean {
		_ = h.eng.Stop(ctx, "test restart")
	}
	h.st.Close()
	h.start()
}

func (h *harness) cycle() CycleReport {
	h.t.Helper()
	rep := h.eng.Cycle(ctx, nil)
	if rep.StoreError != "" {
		h.t.Fatalf("cycle store error: %s", rep.StoreError)
	}
	return rep
}

// run advances the clock in steps and cycles after each step.
func (h *harness) run(minutes, step int) {
	h.t.Helper()
	for m := 0; m < minutes; m += step {
		h.clock.Advance(time.Duration(step) * time.Minute)
		h.cycle()
	}
}

func (h *harness) active(typ string) []domain.Finding {
	h.t.Helper()
	fs, err := h.st.Findings(ctx, store.FindingFilter{Type: typ,
		States: []domain.FindingState{domain.FindingOpen, domain.FindingAcknowledged, domain.FindingSnoozed}})
	if err != nil {
		h.t.Fatal(err)
	}
	return fs
}

func (h *harness) all(typ string) []domain.Finding {
	h.t.Helper()
	fs, err := h.st.Findings(ctx, store.FindingFilter{Type: typ})
	if err != nil {
		h.t.Fatal(err)
	}
	return fs
}

func (h *harness) mustOneActive(typ string) domain.Finding {
	h.t.Helper()
	fs := h.active(typ)
	if len(fs) != 1 {
		h.t.Fatalf("want 1 active %s finding, got %d: %+v", typ, len(fs), fs)
	}
	return fs[0]
}

// stalledSetup creates a public stalled transfer with a matching Sonarr row
// and runs until the stall is diagnosed.
func stalledSetup(t *testing.T) *harness {
	h := newHarness(t, baseConfig)
	h.client.SetTransfers(fake.Transfer(hash, "Example.Show.S01E01", domain.StateDownloading, 2*gib, 10*gib, t0))
	h.mgr.SetQueue(fake.Episode(1, hash, "Example.Show.S01E01", "Example Show", 1, 1))
	h.cycle()
	h.run(120, 10)
	return h
}

func TestStallDiagnosedWithCorrelationAndBaseline(t *testing.T) {
	h := stalledSetup(t)
	f := h.mustOneActive(domain.FindingTransferStalled)
	if f.Subject.ClientID != "qbit" || f.Subject.NativeID != hash || len(f.Subject.Content) != 1 || f.Subject.Content[0] != "Example Show S01E01" {
		t.Fatalf("subject %+v", f.Subject)
	}
	if f.Detail.Baseline[baseVerified] != 2*gib || f.Detail.Baseline[baseWanted] != 10*gib {
		t.Fatalf("baseline %+v", f.Detail.Baseline)
	}
	if f.Detail.CurrentState != "" {
		t.Fatalf("freshly qualified finding must not carry a suspended note: %q", f.Detail.CurrentState)
	}
	for _, s := range f.Detail.NextSteps {
		if strings.Contains(s, "Remove from client") || strings.Contains(s, "keep the partial data") {
			t.Fatalf("guidance promises unverified preservation: %q", s)
		}
	}
	q, _ := h.st.QueueItems(ctx, store.QueueFilter{ManagerID: "sonarr"})
	if len(q) != 1 || q[0].MatchStatus != store.MatchMatched || q[0].MatchNativeID != hash {
		t.Fatalf("correlation %+v", q)
	}
}

func TestStallIsNotResolvedBySuspensionOrMissingEvidence(t *testing.T) {
	transitions := []struct {
		name  string
		apply func(h *harness)
		note  string
	}{
		{"paused", func(h *harness) {
			h.client.Update(hash, func(t *domain.Transfer) { t.StateClass, t.NativeState = domain.StatePaused, "stoppedDL" })
		}, "paused"},
		{"queued", func(h *harness) {
			h.client.Update(hash, func(t *domain.Transfer) { t.StateClass, t.NativeState = domain.StateQueued, "queuedDL" })
		}, "active slot"},
		{"checking", func(h *harness) {
			h.client.Update(hash, func(t *domain.Transfer) { t.StateClass, t.NativeState = domain.StateChecking, "checkingDL" })
		}, "checking"},
		{"verified progress unknown", func(h *harness) {
			h.client.Update(hash, func(t *domain.Transfer) { t.VerifiedBytes = domain.UnknownValue[int64]("glitch") })
		}, "unavailable"},
		{"observation gap", func(h *harness) { h.clock.Advance(45 * time.Minute) }, "gap"},
		{"wall clock jump", func(h *harness) { h.clock.JumpWall(5 * time.Minute) }, "jumped"},
		{"recheck lowers verified data", func(h *harness) {
			h.client.Update(hash, func(t *domain.Transfer) { t.VerifiedBytes = domain.KnownValue(gib) })
		}, "decreased"},
	}
	for _, tc := range transitions {
		t.Run(tc.name, func(t *testing.T) {
			h := stalledSetup(t)
			before := h.mustOneActive(domain.FindingTransferStalled)
			tc.apply(h)
			h.clock.Advance(10 * time.Minute)
			h.cycle()
			after := h.mustOneActive(domain.FindingTransferStalled)
			if after.ID != before.ID {
				t.Fatalf("finding replaced: %d → %d", before.ID, after.ID)
			}
			if !strings.Contains(after.Detail.CurrentState, tc.note) {
				t.Fatalf("current state %q does not explain %q", after.Detail.CurrentState, tc.note)
			}
			if after.Detail.Baseline[baseVerified] != 2*gib {
				t.Fatalf("baseline changed: %+v", after.Detail.Baseline)
			}
			if len(h.all(domain.FindingTransferStalled)) != 1 {
				t.Fatal("a resolved finding was created")
			}
			// Several more cycles in the same condition keep it open without event spam.
			h.run(30, 10)
			if got := h.mustOneActive(domain.FindingTransferStalled); got.ID != before.ID {
				t.Fatal("finding replaced while suspended")
			}
			evs, _ := h.st.FindingEvents(ctx, before.ID)
			if len(evs) > 4 {
				t.Fatalf("repeated suspended cycles produced %d events: %+v", len(evs), evs)
			}
		})
	}
}

func TestStallResolvesOnlyOnVerifiedEvidence(t *testing.T) {
	t.Run("verified progress", func(t *testing.T) {
		h := stalledSetup(t)
		h.client.Update(hash, func(t *domain.Transfer) { t.VerifiedBytes = domain.KnownValue(2*gib + 4<<20) })
		h.run(10, 10)
		if len(h.active(domain.FindingTransferStalled)) != 0 {
			t.Fatal("verified progress must resolve the stall")
		}
		f := h.all(domain.FindingTransferStalled)[0]
		if !strings.HasPrefix(f.Resolution, "Verified progress") {
			t.Fatalf("resolution %q", f.Resolution)
		}
	})
	t.Run("progress after pause and resume", func(t *testing.T) {
		h := stalledSetup(t)
		h.client.Update(hash, func(t *domain.Transfer) { t.StateClass = domain.StatePaused })
		h.run(20, 10)
		h.client.Update(hash, func(t *domain.Transfer) {
			t.StateClass = domain.StateDownloading
			t.VerifiedBytes = domain.KnownValue(3 * gib)
		})
		h.run(10, 10)
		if len(h.active(domain.FindingTransferStalled)) != 0 {
			t.Fatal("progress beyond the baseline after resume must resolve")
		}
	})
	t.Run("completion", func(t *testing.T) {
		h := stalledSetup(t)
		h.client.Update(hash, func(t *domain.Transfer) {
			t.StateClass, t.NativeState, t.VerifiedBytes = domain.StateComplete, "stalledUP", domain.KnownValue(10*gib)
		})
		h.run(10, 10)
		f := h.all(domain.FindingTransferStalled)[0]
		if f.State != domain.FindingResolved || !strings.Contains(f.Resolution, "completed") {
			t.Fatalf("%s %q", f.State, f.Resolution)
		}
	})
	t.Run("authoritative disappearance", func(t *testing.T) {
		h := stalledSetup(t)
		h.client.Remove(hash)
		h.run(10, 10)
		f := h.all(domain.FindingTransferStalled)[0]
		if f.State != domain.FindingResolved || !strings.Contains(f.Resolution, "no longer present") || len(f.Detail.Limitations) == 0 {
			t.Fatalf("%s %q %v", f.State, f.Resolution, f.Detail.Limitations)
		}
		if !strings.Contains(f.Detail.Limitations[len(f.Detail.Limitations)-1], "cannot tell") {
			t.Fatal("unknown cause of disappearance must be recorded")
		}
	})
	t.Run("partial listing omitting the transfer", func(t *testing.T) {
		h := stalledSetup(t)
		h.client.Remove(hash)
		h.client.Partial(true)
		h.run(30, 10)
		h.mustOneActive(domain.FindingTransferStalled)
	})
	t.Run("client read failure", func(t *testing.T) {
		h := stalledSetup(t)
		h.client.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "connection refused"})
		h.run(60, 10)
		f := h.mustOneActive(domain.FindingTransferStalled)
		if f.Detail.CurrentState != "" {
			t.Fatal("a dependency failure must not modify the finding")
		}
		h.mustOneActive(domain.FindingConnectionFailure)
	})
	t.Run("file selection change rebaselines", func(t *testing.T) {
		h := stalledSetup(t)
		h.client.Update(hash, func(t *domain.Transfer) {
			t.WantedBytes, t.VerifiedBytes = domain.KnownValue(8*gib), domain.KnownValue(gib)
		})
		h.run(10, 10)
		f := h.mustOneActive(domain.FindingTransferStalled)
		if f.Detail.Baseline[baseWanted] != 8*gib || !strings.Contains(f.Detail.CurrentState, "File selection changed") {
			t.Fatalf("%+v %q", f.Detail.Baseline, f.Detail.CurrentState)
		}
		h.client.Update(hash, func(t *domain.Transfer) { t.VerifiedBytes = domain.KnownValue(gib + 1<<20) })
		h.run(10, 10)
		if len(h.active(domain.FindingTransferStalled)) != 0 {
			t.Fatal("progress on the new selection must resolve")
		}
	})
}

func TestStallRecurrenceLinksOccurrences(t *testing.T) {
	h := stalledSetup(t)
	first := h.mustOneActive(domain.FindingTransferStalled)
	h.client.Update(hash, func(t *domain.Transfer) { t.VerifiedBytes = domain.KnownValue(3 * gib) })
	h.run(10, 10)
	h.run(100, 10) // flat again for a full window
	again := h.mustOneActive(domain.FindingTransferStalled)
	if again.ID == first.ID || again.PreviousID == nil || *again.PreviousID != first.ID || again.Occurrence != 2 {
		t.Fatalf("recurrence %+v", again)
	}
	if again.Detail.Baseline[baseVerified] != 3*gib {
		t.Fatalf("recurrence baseline %+v", again.Detail.Baseline)
	}
}

func TestAcknowledgedAndSnoozedStallsKeepStateWhileSuspended(t *testing.T) {
	h := stalledSetup(t)
	f := h.mustOneActive(domain.FindingTransferStalled)
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		_, err := findingsAcknowledge(tx, f.ID, h.clock.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.client.Update(hash, func(t *domain.Transfer) { t.StateClass = domain.StatePaused })
	h.run(20, 10)
	got := h.mustOneActive(domain.FindingTransferStalled)
	if got.State != domain.FindingAcknowledged {
		t.Fatalf("state %s", got.State)
	}
}

func TestMetadataTimeoutLifecycle(t *testing.T) {
	h := newHarness(t, baseConfig)
	h.client.SetTransfers(fake.Transfer(hash, "Magnet.Only", domain.StateMetadata, 0, 0, t0))
	h.cycle()
	h.run(90, 10)
	h.mustOneActive(domain.FindingMetadataTimeout)
	// Paused while still without metadata: carried, not resolved.
	h.client.Update(hash, func(t *domain.Transfer) { t.StateClass, t.NativeState = domain.StatePaused, "stoppedDL" })
	h.run(10, 10)
	f := h.mustOneActive(domain.FindingMetadataTimeout)
	if !strings.Contains(f.Detail.CurrentState, "Metadata has not been acquired") {
		t.Fatalf("note %q", f.Detail.CurrentState)
	}
	// Metadata acquired.
	h.client.SetTransfers(fake.Transfer(hash, "Magnet.Only", domain.StateDownloading, 0, 4*gib, t0))
	h.run(10, 10)
	if len(h.active(domain.FindingMetadataTimeout)) != 0 {
		t.Fatal("acquired metadata must resolve the finding")
	}
}

func TestClientErrorRequiresConsecutiveObservations(t *testing.T) {
	h := newHarness(t, baseConfig)
	h.client.SetTransfers(fake.Transfer(hash, "Broken", domain.StateError, gib, 2*gib, t0))
	h.cycle()
	if len(h.active(domain.FindingTransferError)) != 0 {
		t.Fatal("a single error observation must not open a finding")
	}
	h.run(10, 10)
	h.mustOneActive(domain.FindingTransferError)
	h.client.Update(hash, func(t *domain.Transfer) { t.StateClass, t.NativeState = domain.StateDownloading, "stalledDL" })
	h.run(10, 10)
	if f := h.mustOneActive(domain.FindingTransferError); !strings.Contains(f.Detail.CurrentState, "awaiting") {
		t.Fatalf("note %q", f.Detail.CurrentState)
	}
	h.run(10, 10)
	if len(h.active(domain.FindingTransferError)) != 0 {
		t.Fatal("two non-error observations must resolve")
	}
}

func TestConnectionFailureThresholdsAndRecovery(t *testing.T) {
	h := stalledSetup(t)
	h.client.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "connection refused"})
	h.run(10, 10)
	if len(h.active(domain.FindingConnectionFailure)) != 0 {
		t.Fatal("one transient failure must not open a finding")
	}
	h.run(10, 10)
	c := h.mustOneActive(domain.FindingConnectionFailure)
	if c.Severity != domain.SeverityWarning || !strings.Contains(c.Title, "unreachable") {
		t.Fatalf("%s %q", c.Severity, c.Title)
	}
	// Unrelated healthy connections keep being observed.
	if reads := h.mgr.Reads(); reads < 14 {
		t.Fatalf("manager reads %d", reads)
	}
	h.client.Fail(nil)
	h.run(60, 10) // backoff may skip some cycles
	if len(h.active(domain.FindingConnectionFailure)) != 0 {
		t.Fatal("successful read must resolve the connection finding")
	}
	h.mustOneActive(domain.FindingTransferStalled)
}

func TestUnauthorizedBacksOffAndOpensImmediately(t *testing.T) {
	h := newHarness(t, baseConfig)
	h.client.Fail(&adapter.Error{Class: adapter.ErrUnauthorized, Op: "fake", Message: "HTTP 401"})
	h.cycle()
	f := h.mustOneActive(domain.FindingConnectionFailure)
	if f.Severity != domain.SeverityError {
		t.Fatalf("severity %s", f.Severity)
	}
	before := h.client.Lists()
	h.run(50, 10)
	if h.client.Lists() != before {
		t.Fatalf("unauthorized connection retried within the backoff: %d → %d", before, h.client.Lists())
	}
	h.run(20, 10)
	if h.client.Lists() == before {
		t.Fatal("connection never retried after backoff")
	}
	// A configuration change resets backoff on the next start.
	h.client.Fail(&adapter.Error{Class: adapter.ErrUnauthorized, Op: "fake", Message: "HTTP 401"})
	h.cycle()
	n := h.client.Lists()
	h.cfg.Revision = "changed"
	h.restart(true)
	h.clock.Advance(time.Minute)
	h.cycle()
	if h.client.Lists() != n+1 {
		t.Fatal("configuration change must reset backoff")
	}
}

func TestStateStoreFailureSkipsCycleAndRecovers(t *testing.T) {
	h := stalledSetup(t)
	reads := h.client.Lists()
	h.st.InjectWriteFault(errors.New("disk full"))
	h.clock.Advance(10 * time.Minute)
	rep := h.eng.Cycle(ctx, nil)
	if rep.StoreError == "" || !strings.Contains(rep.StoreError, "disk full") {
		t.Fatalf("store error not reported: %+v", rep)
	}
	if h.client.Lists() != reads {
		t.Fatal("no external work may run when state cannot be persisted")
	}
	if _, err := h.eng.LastCycle(); err == nil {
		t.Fatal("engine must expose the store error")
	}
	h.st.InjectWriteFault(nil)
	h.run(10, 10)
	if _, err := h.eng.LastCycle(); err != nil {
		t.Fatalf("store error not cleared: %v", err)
	}
	h.mustOneActive(domain.FindingTransferStalled)
}

func TestRestartContinuity(t *testing.T) {
	h := newHarness(t, baseConfig)
	h.client.SetTransfers(fake.Transfer(hash, "Example.Show.S01E01", domain.StateDownloading, 2*gib, 10*gib, t0))
	h.cycle()
	h.run(70, 10)
	h.restart(false) // abrupt termination
	h.run(50, 10)
	h.mustOneActive(domain.FindingTransferStalled)
	audit, _ := h.st.RecentAudit(ctx, 10)
	found := false
	for _, a := range audit {
		found = found || a.Kind == "startup.reconcile"
	}
	if !found {
		t.Fatal("abrupt termination must be recorded at startup")
	}
	runs, _ := h.st.RecentRuns(ctx, 5)
	if len(runs) != 2 || runs[1].StopReason != "terminated without clean shutdown" {
		t.Fatalf("runs %+v", runs)
	}
}

func TestQueueCorrelationOwnership(t *testing.T) {
	t.Run("shared infohash with explicit mapping", func(t *testing.T) {
		h := newHarness(t, baseConfig)
		tr := fake.Transfer(hash, "Shared", domain.StateDownloading, gib, 2*gib, t0)
		h.client.SetTransfers(tr)
		h.other.SetTransfers(tr)
		h.mgr.SetQueue(fake.Episode(1, hash, "Shared", "Show", 1, 1))
		h.cycle()
		q, _ := h.st.QueueItems(ctx, store.QueueFilter{})
		if q[0].MatchStatus != store.MatchMatched || q[0].MatchClientID != "qbit" || !strings.Contains(q[0].MatchNote, "shared") {
			t.Fatalf("%+v", q[0])
		}
	})
	t.Run("ambiguous without mapping", func(t *testing.T) {
		h := newHarness(t, strings.Replace(baseConfig, `download_clients = { "qBittorrent" = "qbit" }`, "", 1))
		tr := fake.Transfer(hash, "Shared", domain.StateDownloading, gib, 2*gib, t0)
		h.client.SetTransfers(tr)
		h.other.SetTransfers(tr)
		h.mgr.SetQueue(fake.Episode(1, hash, "Shared", "Show", 1, 1))
		h.cycle()
		h.mustOneActive(domain.FindingQueueAmbiguous)
	})
	t.Run("hash only without mapping", func(t *testing.T) {
		h := newHarness(t, strings.Replace(baseConfig, `download_clients = { "qBittorrent" = "qbit" }`, "", 1))
		h.client.SetTransfers(fake.Transfer(hash, "One", domain.StateDownloading, gib, 2*gib, t0))
		h.mgr.SetQueue(fake.Episode(1, hash, "One", "Show", 1, 1))
		h.cycle()
		q, _ := h.st.QueueItems(ctx, store.QueueFilter{})
		if q[0].MatchStatus != store.MatchHashOnly {
			t.Fatalf("%+v", q[0])
		}
	})
	t.Run("not found needs persistence; stale client is unknown", func(t *testing.T) {
		h := newHarness(t, baseConfig)
		h.mgr.SetQueue(fake.Episode(1, hash, "Missing", "Show", 1, 1))
		h.cycle()
		if len(h.active(domain.FindingQueueUnmatched)) != 0 {
			t.Fatal("not-found must persist across cycles before a finding")
		}
		h.run(10, 10)
		h.mustOneActive(domain.FindingQueueUnmatched)
		// Client failure: correlation becomes unknown; the finding is kept, not resolved.
		h.client.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
		h.run(30, 10)
		q, _ := h.st.QueueItems(ctx, store.QueueFilter{})
		if q[0].MatchStatus != store.MatchClientUnavailable {
			t.Fatalf("match %s", q[0].MatchStatus)
		}
		h.mustOneActive(domain.FindingQueueUnmatched)
		// The transfer appears once the client is back: resolved by evidence.
		h.client.Fail(nil)
		h.client.SetTransfers(fake.Transfer(hash, "Missing", domain.StateDownloading, gib, 2*gib, t0))
		h.run(60, 10)
		if len(h.active(domain.FindingQueueUnmatched)) != 0 {
			t.Fatal("finding should resolve once the transfer is listed")
		}
	})
}

func TestImportAttentionSettlesAndSurvivesManagerFailure(t *testing.T) {
	h := newHarness(t, baseConfig)
	blocked := func(id int64, ep int64) domain.QueueItem {
		q := fake.Episode(id, hash, "Show.S01.Pack", "Show", 1, ep)
		q.TrackedState, q.TrackedStatus = domain.KnownValue("importBlocked"), domain.KnownValue("warning")
		q.StatusMessages = []domain.StatusMessage{{Title: "Show.S01.Pack", Messages: []string{"One or more episodes expected in this release were not imported or missing from the release"}}}
		return q
	}
	h.client.SetTransfers(fake.Transfer(hash, "Show.S01.Pack", domain.StateComplete, 2*gib, 2*gib, t0))
	h.mgr.SetQueue(blocked(1, 1), blocked(2, 2))
	h.cycle()
	h.run(20, 10)
	if len(h.active(domain.FindingImportAttention)) != 0 {
		t.Fatal("import attention must wait for the settling period")
	}
	h.run(10, 10)
	f := h.mustOneActive(domain.FindingImportAttention)
	if len(f.Subject.Content) != 2 || f.Subject.ClientID != "qbit" {
		t.Fatalf("season pack grouping %+v", f.Subject)
	}
	h.mgr.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
	h.run(60, 10)
	h.mustOneActive(domain.FindingImportAttention)
	h.mgr.Fail(nil)
	h.mgr.Partial(true)
	h.mgr.SetQueue()
	h.run(30, 10)
	h.mustOneActive(domain.FindingImportAttention)
	h.mgr.Partial(false)
	h.run(10, 10)
	f = h.all(domain.FindingImportAttention)[0]
	if f.State != domain.FindingResolved || !strings.Contains(f.Resolution, "No longer in") {
		t.Fatalf("%s %q", f.State, f.Resolution)
	}
}

func TestManagerHealthFindings(t *testing.T) {
	h := newHarness(t, baseConfig)
	h.mgr.SetHealth(domain.HealthItem{Source: "IndexerSearchCheck", Type: "warning", Message: "All search-capable indexers are temporarily unavailable"},
		domain.HealthItem{Source: "UpdateCheck", Type: "notice", Message: "Update available"})
	h.cycle()
	f := h.mustOneActive(domain.FindingManagerHealth)
	if !strings.Contains(f.Summary, "incomplete source coverage") {
		t.Fatalf("summary %q", f.Summary)
	}
	h.mgr.SetHealth()
	h.run(10, 10)
	if len(h.active(domain.FindingManagerHealth)) != 0 {
		t.Fatal("cleared health check must resolve")
	}
}

func TestNoProgressDiagnosisSurvivesManagerOutage(t *testing.T) {
	h := newHarness(t, baseConfig)
	h.client.SetTransfers(fake.Transfer(hash, "Example", domain.StateDownloading, gib, 2*gib, t0))
	h.mgr.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
	h.cycle()
	h.run(120, 10)
	h.mustOneActive(domain.FindingTransferStalled)
	if len(h.active(domain.FindingConnectionFailure)) != 1 {
		t.Fatal("manager outage should be its own finding")
	}
}

func findingsAcknowledge(tx *store.Tx, id int64, now time.Time) (*domain.Finding, error) {
	return findings.Acknowledge(tx, id, "test", now)
}

func TestBackoffIsBounded(t *testing.T) {
	iv := 10 * time.Minute
	if d := backoff(adapter.ErrTransient, 1, iv, 0); d != iv {
		t.Fatalf("first transient failure must keep cadence, got %s", d)
	}
	if d := backoff(adapter.ErrTransient, 50, iv, 0); d != time.Hour {
		t.Fatalf("transient ceiling %s", d)
	}
	if d := backoff(adapter.ErrUnauthorized, 1, iv, 0); d < time.Hour {
		t.Fatalf("auth failures must not retry rapidly: %s", d)
	}
	if d := backoff(adapter.ErrRateLimited, 1, iv, 3*time.Hour); d != 3*time.Hour {
		t.Fatalf("retry-after not honoured: %s", d)
	}
}

func TestRadarrManagerCorrelationAndWording(t *testing.T) {
	cfgSrc := baseConfig + `
[[radarr]]
id = "radarr"
name = "Radarr"
url = "http://127.0.0.1:7878"
api_key = "env:Y"
download_clients = { "qBittorrent" = "qbit" }
`
	cfg, _, err := config.Parse([]byte(cfgSrc), "/x/h.toml")
	if err != nil {
		t.Fatal(err)
	}
	clock := fake.NewClock(t0)
	client, rad := fake.NewClient("qbit"), fake.NewRadarr("radarr")
	st, err := store.Open(ctx, t.TempDir(), store.Options{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conns := &Connections{Clients: []adapter.DownloadClient{client}, Managers: []adapter.MediaManager{rad}, Meta: map[string]ConnMeta{
		"qbit":   {ID: "qbit", Name: "qBittorrent", Kind: adapter.KindQBittorrent, Enabled: true},
		"radarr": {ID: "radarr", Name: "Radarr", BaseURL: "http://127.0.0.1:7878", Kind: adapter.KindRadarr, Enabled: true, DownloadClients: cfg.Radarr[0].DownloadClients},
	}}
	e, err := New(ctx, Options{Config: cfg, Store: st, Conns: conns, Clock: clock, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	client.SetTransfers(fake.Transfer(hash, "Example.Film.2024", domain.StateDownloading, gib, 4*gib, t0))
	mv := fake.Movie(1, hash, "Example.Film.2024", "Example Film", 2024)
	mv.TrackedState, mv.TrackedStatus = domain.KnownValue("importPending"), domain.KnownValue("warning")
	rad.SetQueue(mv)
	for i := 0; i <= 12; i++ {
		e.Cycle(ctx, nil)
		clock.Advance(10 * time.Minute)
	}
	fs, _ := st.Findings(ctx, store.FindingFilter{States: []domain.FindingState{domain.FindingOpen}})
	var stall, imp *domain.Finding
	for i := range fs {
		switch fs[i].Type {
		case domain.FindingTransferStalled:
			stall = &fs[i]
		case domain.FindingImportAttention:
			imp = &fs[i]
		}
	}
	if stall == nil || len(stall.Subject.Content) != 1 || stall.Subject.Content[0] != "Example Film (2024)" {
		t.Fatalf("stall %+v", stall)
	}
	if !strings.Contains(strings.Join(stall.Detail.NextSteps, " "), "Radarr → Activity → Queue") {
		t.Fatalf("steps must name Radarr: %v", stall.Detail.NextSteps)
	}
	if imp == nil || !strings.HasPrefix(imp.Title, "Radarr import pending") {
		t.Fatalf("import %+v", imp)
	}
	for _, l := range imp.Detail.Limitations {
		if strings.Contains(l, "season-pack") {
			t.Fatal("season-pack limitation must be Sonarr-only")
		}
	}
}

// A retained transfer stays counted until a complete listing no longer has
// it; an incomplete or failed listing never releases it.
func TestRetainedTransferReleasedOnlyWhenGone(t *testing.T) {
	h := newHarness(t, baseConfig)
	h.client.SetTransfers(fake.Transfer(hash, "Example.Show.S01E01", domain.StatePaused, 2*gib, 10*gib, t0))
	h.cycle()
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		return tx.RecordRetained(store.RetainedTransfer{ClientID: "qbit", NativeID: hash, IntentID: 1, RetainedAt: t0})
	}); err != nil {
		t.Fatal(err)
	}
	h.client.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "connection refused"})
	h.run(10, 10)
	if u, _ := h.st.RetainedUsage(ctx); u.Count != 1 {
		t.Fatalf("released on a failed listing: %+v", u)
	}
	h.client.Fail(nil)
	h.client.SetTransfers()
	h.run(10, 10)
	ret, _ := h.st.RetainedTransfers(ctx, true, 0)
	if len(ret) != 1 || ret[0].ReleasedAt == nil || ret[0].ReleasedReason != "no longer listed by the client" {
		t.Fatalf("retained %+v", ret)
	}
	if u, _ := h.st.RetainedUsage(ctx); u.Count != 0 {
		t.Fatalf("usage %+v", u)
	}
}

// After a verified recovery the engine records the replacement grab, its
// progress, and completion as separate outcomes, only from complete reads.
func TestRecoveryOutcomesTrackedSeparately(t *testing.T) {
	const newHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const earlier = "cccccccccccccccccccccccccccccccccccccccc"
	h := newHarness(t, baseConfig)
	// A different release for the same episode was already queued before
	// the recovery: it is not the replacement.
	h.mgr.SetQueue(fake.Episode(3, earlier, "Earlier.Release", "Example Show", 1, 1))
	h.cycle()
	h.clock.Advance(time.Minute)
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		return tx.CreateOutcome(store.RecoveryOutcome{IntentID: 7, ManagerID: "sonarr", Content: []string{"episode:101"}, OldDownloadID: hash,
			StartedAt: h.clock.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	state := func() store.RecoveryOutcome {
		t.Helper()
		m, err := h.st.Outcomes(ctx, []int64{7})
		if err != nil {
			t.Fatal(err)
		}
		return m[7]
	}
	// The old release's row reappearing is not a replacement either.
	h.mgr.SetQueue(fake.Episode(1, hash, "Old.Release", "Example Show", 1, 1), fake.Episode(3, earlier, "Earlier.Release", "Example Show", 1, 1))
	h.run(10, 10)
	if o := state(); o.State != store.OutcomeAwaitingGrab {
		t.Fatalf("old release counted as replacement: %+v", o)
	}
	// Nothing for a day: reported as not observed, still watched.
	h.mgr.SetQueue()
	h.run(25*60, 60)
	if o := state(); o.State != store.OutcomeNoGrabYet || !strings.Contains(o.Note, "not evidence that no release exists") {
		t.Fatalf("no grab: %+v", o)
	}
	// A failed manager read changes nothing.
	h.mgr.SetQueue(fake.Episode(2, newHash, "New.Release", "Example Show", 1, 1))
	h.mgr.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
	h.run(10, 10)
	if o := state(); o.State != store.OutcomeNoGrabYet {
		t.Fatalf("moved on a failed read: %+v", o)
	}
	h.mgr.Fail(nil)
	h.client.SetTransfers(fake.Transfer(newHash, "New.Release", domain.StateDownloading, 1*gib, 10*gib, t0))
	h.run(20, 10)
	o := state()
	if o.State != store.OutcomeGrabbed || o.NewDownloadID != newHash || o.NewNativeID != newHash || o.GrabVerified == nil {
		t.Fatalf("grab: %+v", o)
	}
	h.client.Update(newHash, func(tr *domain.Transfer) { tr.VerifiedBytes = domain.KnownValue[int64](3 * gib) })
	h.run(10, 10)
	if o := state(); o.State != store.OutcomeProgressing {
		t.Fatalf("progress: %+v", o)
	}
	h.client.Update(newHash, func(tr *domain.Transfer) {
		tr.StateClass, tr.VerifiedBytes = domain.StateComplete, domain.KnownValue[int64](10*gib)
	})
	h.run(10, 10)
	if o := state(); o.State != store.OutcomeCompleted || o.CompletedAt == nil || o.GrabbedAt == nil || o.ProgressAt == nil {
		t.Fatalf("completion: %+v", o)
	}
}

// The replacement search is followed from the manager's command list,
// separately from the grab, and only while it is unsettled; a failed
// command read changes nothing and does not fail the manager's read.
func TestReplacementSearchFollowedFromCommandList(t *testing.T) {
	h := newHarness(t, baseConfig)
	h.cycle()
	since := h.clock.Now()
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		return tx.CreateOutcome(store.RecoveryOutcome{IntentID: 9, ManagerID: "sonarr", Content: []string{"episode:101"}, OldDownloadID: hash,
			StartedAt: since.Add(10 * time.Second), Search: store.SearchOutcome{Since: &since}})
	}); err != nil {
		t.Fatal(err)
	}
	state := func() store.RecoveryOutcome {
		t.Helper()
		m, err := h.st.Outcomes(ctx, []int64{9})
		if err != nil {
			t.Fatal(err)
		}
		return m[9]
	}
	h.run(10, 10)
	if o := state(); o.Search.State != store.SearchUnobserved || o.State != store.OutcomeAwaitingGrab {
		t.Fatalf("nothing listed yet: %+v", o)
	}
	queued := since.Add(5 * time.Second)
	search := arr.Command{ID: 77, Name: "EpisodeSearch", Status: "queued", Trigger: "unspecified", Queued: queued, EpisodeIDs: []int64{101}}
	h.mgr.AddCommand(arr.Command{ID: 76, Name: "EpisodeSearch", Status: "queued", Queued: queued, EpisodeIDs: []int64{102}}) // other content
	h.mgr.AddCommand(search)
	h.run(10, 10)
	if o := state(); o.Search.State != store.SearchAccepted || o.Search.CommandID == nil || *o.Search.CommandID != 77 {
		t.Fatalf("accepted: %+v", o.Search)
	}
	// A failed command read changes nothing, and the manager is still read.
	h.mgr.SetCommandsError(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
	rep := h.cycle()
	for _, c := range rep.Connections {
		if c.ID == "sonarr" && !c.OK {
			t.Fatalf("a failed command read failed the manager read: %+v", c)
		}
	}
	if o := state(); o.Search.State != store.SearchAccepted {
		t.Fatalf("moved on a failed read: %+v", o.Search)
	}
	h.mgr.SetCommandsError(nil)
	ended := queued.Add(2 * time.Second)
	search.Status, search.Result, search.Ended, search.Message = "completed", "successful", &ended, "Episode search completed. 0 reports downloaded."
	h.mgr.AddCommand(search)
	h.run(10, 10)
	o := state()
	if o.Search.State != store.SearchCompleted || !strings.Contains(o.Search.Note, "0 reports downloaded") || !strings.Contains(o.Search.Note, "not a grab") {
		t.Fatalf("completed: %+v", o.Search)
	}
	if o.State != store.OutcomeAwaitingGrab {
		t.Fatalf("a completed search moved the grab outcome: %+v", o)
	}
	if need, _ := h.st.ManagersAwaitingSearch(ctx); len(need) != 0 {
		t.Fatalf("command list still read after the search settled: %v", need)
	}
}

// R9: a fresh manager row establishes a grab, never the replacement's
// progress or completion; those need a complete client listing in the
// same cycle, so cached (possibly pre-recovery) client facts never count.
func TestOutcomeProgressRequiresFreshClient(t *testing.T) {
	const replacement = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	h := newHarness(t, baseConfig)
	h.client.SetTransfers(fake.Transfer(replacement, "Replacement", domain.StateComplete, 10*gib, 10*gib, t0))
	h.cycle()
	h.clock.Advance(time.Minute)
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		return tx.CreateOutcome(store.RecoveryOutcome{IntentID: 31, ManagerID: "sonarr", Content: []string{"episode:101"}, OldDownloadID: hash, StartedAt: h.clock.Now()})
	}); err != nil {
		t.Fatal(err)
	}
	state := func() store.RecoveryOutcome {
		t.Helper()
		m, err := h.st.Outcomes(ctx, []int64{31})
		if err != nil {
			t.Fatal(err)
		}
		return m[31]
	}
	h.mgr.SetQueue(fake.Episode(2, replacement, "Replacement", "Example Show", 1, 1))
	h.client.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "list", Message: "offline"})
	h.run(10, 10)
	if o := state(); o.State != store.OutcomeGrabbed || o.CompletedAt != nil || o.GrabVerified != nil {
		t.Fatalf("offline client: want a grab only, got %+v", o)
	}
	// Repeated failures put the client in backoff (it is skipped, not
	// read) while the manager stays healthy: still only a grab.
	lists := h.client.Lists()
	h.run(120, 10)
	if h.client.Lists()-lists >= 12 {
		t.Fatalf("setup: client never backed off (%d reads in 12 cycles)", h.client.Lists()-lists)
	}
	if o := state(); o.State != store.OutcomeGrabbed || o.CompletedAt != nil {
		t.Fatalf("backed-off client: %+v", o)
	}
	// An incomplete listing is not fresh client evidence either.
	h.client.Fail(nil)
	h.client.Partial(true)
	h.run(10, 10)
	if o := state(); o.State != store.OutcomeGrabbed || o.CompletedAt != nil {
		t.Fatalf("incomplete listing: %+v", o)
	}
	// A complete listing after the grab establishes completion now.
	h.client.Partial(false)
	h.run(10, 10)
	o := state()
	if o.State != store.OutcomeCompleted || o.CompletedAt == nil || !o.CompletedAt.After(*o.GrabbedAt) {
		t.Fatalf("fresh client: %+v", o)
	}
}

// R10: the download and search outcomes settle independently; a terminal
// download keeps its unsettled search followed (or expired within the
// bounded window) and the command list stops being read once both settle.
func TestSearchSettlesIndependentlyOfDownload(t *testing.T) {
	const replacement = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	h := newHarness(t, baseConfig)
	h.cycle()
	since := h.clock.Now()
	if err := h.st.Write(ctx, func(tx *store.Tx) error {
		if err := tx.CreateOutcome(store.RecoveryOutcome{IntentID: 32, ManagerID: "sonarr", Content: []string{"episode:101"}, OldDownloadID: hash,
			StartedAt: since, Search: store.SearchOutcome{Since: &since}}); err != nil {
			return err
		}
		// Tracking of this download already expired; its search is new.
		return tx.CreateOutcome(store.RecoveryOutcome{IntentID: 33, ManagerID: "sonarr", Content: []string{"episode:102"}, OldDownloadID: "dd",
			StartedAt: since.Add(-8 * 24 * time.Hour), Search: store.SearchOutcome{Since: &since}})
	}); err != nil {
		t.Fatal(err)
	}
	state := func(id int64) store.RecoveryOutcome {
		t.Helper()
		m, err := h.st.Outcomes(ctx, []int64{id})
		if err != nil {
			t.Fatal(err)
		}
		return m[id]
	}
	search := arr.Command{ID: 77, Name: "EpisodeSearch", Status: "queued", Trigger: "unspecified", Queued: since, EpisodeIDs: []int64{101}}
	h.mgr.AddCommand(search)
	h.mgr.SetQueue(fake.Episode(2, replacement, "Replacement", "Example Show", 1, 1))
	h.client.SetTransfers(fake.Transfer(replacement, "Replacement", domain.StateComplete, 10*gib, 10*gib, t0))
	h.run(10, 10)
	if o := state(32); o.State != store.OutcomeCompleted || o.Search.State != store.SearchAccepted {
		t.Fatalf("setup: %+v", o)
	}
	if o := state(33); o.State != store.OutcomeUntracked || o.Search.State != store.SearchUnobserved {
		t.Fatalf("setup (expired download): %+v", o)
	}
	// Survives a restart and a command-list outage.
	h.restart(false)
	h.mgr.SetCommandsError(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
	h.run(10, 10)
	if o := state(32); o.Search.State != store.SearchAccepted {
		t.Fatalf("moved on a failed command read: %+v", o.Search)
	}
	h.mgr.SetCommandsError(nil)
	search.Status = "completed"
	h.mgr.AddCommand(search)
	h.run(10, 10)
	if o := state(32); o.Search.State != store.SearchCompleted || o.State != store.OutcomeCompleted {
		t.Fatalf("search after a completed download: %+v", o)
	}
	// The unobserved search of the expired download ends within its window.
	h.run(70, 10)
	if o := state(33); o.Search.State != store.SearchNotObserved || o.State != store.OutcomeUntracked {
		t.Fatalf("search after an expired download: %+v", o)
	}
	if need, _ := h.st.ManagersAwaitingSearch(ctx); len(need) != 0 {
		t.Fatalf("command list still read after both outcomes settled: %v", need)
	}
}
