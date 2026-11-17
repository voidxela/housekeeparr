package app_test

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
	"github.com/voidxela/housekeeparr/internal/workflow"
)

// The automatic path end to end, deterministically: the real engine with
// an injected clock diagnoses sustained no-progress from observations; the
// real dispatcher, planner, coordinator, and workflow executor act on
// simulated applications (internal/fake). No wall-clock stall window is
// waited out. This establishes the pipeline's own behaviour, not any real
// application's: that is what the disposable-instance runs are for.

const (
	gib   = int64(1) << 30
	hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	hashC = "cccccccccccccccccccccccccccccccccccccccc"
	hashD = "dddddddddddddddddddddddddddddddddddddddd"
)

var t0 = time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)

const autoConfig = `
version = 1
mode = "%MODE%"
[automation.workflows]
no_progress_recovery = true
[automation.limits]
acquisitions = %ACQ%
acquisition_window = "1h"
correlated_stalls = %CORR%
[polling]
interval = "10m"
jitter = "0s"
[[sonarr]]
id = "sonarr"
url = "http://127.0.0.1:8989"
api_key = "env:X"
download_clients = { "qBittorrent" = "qbit" }
[[radarr]]
id = "radarr"
url = "http://127.0.0.1:7878"
api_key = "env:X"
download_clients = { "qBittorrent" = "qbit2" }
[[qbittorrent]]
id = "qbit"
url = "http://127.0.0.1:8080"
[[qbittorrent]]
id = "qbit2"
url = "http://127.0.0.1:8081"
`

type world struct {
	t       *testing.T
	clock   *fake.Clock
	client  *fake.Client
	client2 *fake.Client
	mgr     *fake.Manager
	radarr  *fake.Manager
	st      *store.Store
	eng     *engine.Engine
	svc     *app.Service
}

func newWorld(t *testing.T, mode string, acquisitions int) *world {
	t.Helper()
	return newWorldWith(t, mode, acquisitions, "")
}

// newWorldWith appends extra TOML (rules, overrides) to the configuration.
func newWorldWith(t *testing.T, mode string, acquisitions int, extra string) *world {
	t.Helper()
	corr := "3"
	if acquisitions < 0 {
		// Negative: many acquisitions and no correlated-stall hold, for
		// tests with many simultaneous stalls.
		acquisitions, corr = 100, "1000"
	}
	src := strings.NewReplacer("%MODE%", mode, "%ACQ%", itoa(acquisitions), "%CORR%", corr).Replace(autoConfig) + extra
	cfg, _, err := config.Parse([]byte(src), "/x/housekeeparr.toml")
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, clock: fake.NewClock(t0), client: fake.NewClient("qbit"), client2: fake.NewClient("qbit2"),
		mgr: fake.NewManager("sonarr"), radarr: fake.NewRadarr("radarr")}
	w.st, err = store.Open(context.Background(), t.TempDir(), store.Options{Now: w.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.st.Close() })
	w.mgr.SetClock(w.clock.Now)
	w.radarr.SetClock(w.clock.Now)
	conns := &engine.Connections{Clients: []adapter.DownloadClient{w.client, w.client2}, Managers: []adapter.MediaManager{w.mgr, w.radarr},
		Meta: map[string]engine.ConnMeta{
			"sonarr": {ID: "sonarr", Name: "Sonarr", BaseURL: "http://127.0.0.1:8989", Kind: adapter.KindSonarr, Enabled: true,
				SecretRef: "env:X", DownloadClients: cfg.Sonarr[0].DownloadClients},
			"radarr": {ID: "radarr", Name: "Radarr", BaseURL: "http://127.0.0.1:7878", Kind: adapter.KindRadarr, Enabled: true,
				SecretRef: "env:X", DownloadClients: cfg.Radarr[0].DownloadClients},
			"qbit":  {ID: "qbit", Name: "qBittorrent", BaseURL: "http://127.0.0.1:8080", Kind: adapter.KindQBittorrent, Enabled: true},
			"qbit2": {ID: "qbit2", Name: "qBittorrent 2", BaseURL: "http://127.0.0.1:8081", Kind: adapter.KindQBittorrent, Enabled: true},
		}}
	w.eng, err = engine.New(context.Background(), engine.Options{Config: cfg, Store: w.st, Conns: conns, Clock: w.clock,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "test", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	w.svc = app.New(w.st, cfg, w.clock.Now, w.eng)
	ex := workflow.FromConnections(conns.Clients, conns.Managers)
	ex.Settle, ex.Poll = 50*time.Millisecond, 5*time.Millisecond
	w.svc.SetExecutor(ex)
	saved := action.Tested
	action.Tested = []action.TestedWorkflow{
		{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindSonarr, ManagerVersion: "0.0.0-fake", Client: adapter.KindQBittorrent, ClientVersion: "0.0.0-fake"},
		{Workflow: policy.WorkflowNoProgressRecovery, Manager: adapter.KindRadarr, ManagerVersion: "0.0.0-fake", Client: adapter.KindQBittorrent, ClientVersion: "0.0.0-fake"},
	}
	t.Cleanup(func() { action.Tested = saved })
	return w
}

func itoa(n int) string { return strconv.Itoa(n) }

// stalled adds a public, no-progress download with its Sonarr row.
func (w *world) stalled(hash string, queueID, episode int64) {
	ts, _ := w.client.ListTransfers(context.Background())
	all := append(ts.Transfers, fake.Transfer(hash, "Show.S01E0"+itoa(int(episode)), domain.StateDownloading, 2*gib, 10*gib, t0))
	w.client.SetTransfers(all...)
	snap, _ := w.mgr.Queue(context.Background())
	w.mgr.SetQueue(append(snap.Items, fake.Episode(queueID, hash, "Show.S01E0"+itoa(int(episode)), "Show", 1, episode))...)
}

// run cycles every 10 minutes for the given duration, dispatching after
// each persisted cycle exactly as the worker does.
func (w *world) run(d time.Duration) []app.DispatchReport {
	w.t.Helper()
	var reps []app.DispatchReport
	for end := w.clock.Now().Add(d); w.clock.Now().Before(end); {
		w.clock.Advance(10 * time.Minute)
		rep := w.eng.Cycle(context.Background(), nil)
		if rep.StoreError != "" {
			w.t.Fatal(rep.StoreError)
		}
		r, err := w.svc.DispatchAutomatic(context.Background(), rep.Fresh())
		if err != nil {
			w.t.Fatal(err)
		}
		reps = append(reps, r)
	}
	return reps
}

func (w *world) finding(hash string) domain.Finding {
	w.t.Helper()
	fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: domain.FindingTransferStalled, ClientID: "qbit", NativeID: hash})
	if len(fs) == 0 {
		w.t.Fatalf("no stall finding for %s", hash)
	}
	return fs[0]
}

func TestAutomaticRecoveryEndToEnd(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.stalled(hashA, 1, 1)
	w.run(4 * time.Hour)
	if r := w.mgr.Removals(); len(r) != 1 || r[0] != (arr.RemoveFlags{Blocklist: true}) {
		t.Fatalf("removals %+v (want exactly one, every flag explicit, removeFromClient=false)", r)
	}
	if s := w.client.Stops(); len(s) != 1 || s[0] != hashA {
		t.Fatalf("stops %v", s)
	}
	acts, _ := w.svc.Actions(context.Background(), 10)
	if len(acts) != 1 || acts[0].Origin != "automatic" || acts[0].State != store.IntentSucceeded {
		t.Fatalf("actions %+v", acts)
	}
	f := w.finding(hashA)
	d, _ := w.svc.Finding(context.Background(), f.ID)
	if d.Automation == nil || d.Automation.Result != store.EvalDispatched || d.Automation.IntentID != acts[0].ID {
		t.Fatalf("finding automation %+v", d.Automation)
	}
	ret, _ := w.st.RetainedTransfers(context.Background(), false, 0)
	if len(ret) != 1 || ret[0].NativeID != hashA {
		t.Fatalf("retained %+v", ret)
	}
	oc, _ := w.st.Outcomes(context.Background(), []int64{acts[0].ID})
	if oc[acts[0].ID].State != store.OutcomeAwaitingGrab {
		t.Fatalf("outcome %+v", oc)
	}
	// The manager-native replacement search was observed right after the
	// recovery, as its own outcome (the manager's list drops it minutes
	// later, before the next poll).
	if s := oc[acts[0].ID].Search; s.State != store.SearchCompleted || s.Name != "EpisodeSearch" || !strings.Contains(s.Note, "not a grab") {
		t.Fatalf("replacement search %+v", s)
	}
	// The manager grabs a replacement; it is recorded, and nothing else
	// is sent for this content during later cycles.
	w.stalled(hashB, 2, 1)
	w.client.Update(hashB, func(tr *domain.Transfer) { tr.VerifiedBytes = domain.KnownValue[int64](3 * gib) })
	w.run(time.Hour)
	if oc, _ := w.st.Outcomes(context.Background(), []int64{acts[0].ID}); oc[acts[0].ID].State != store.OutcomeGrabbed || oc[acts[0].ID].NewNativeID != hashB {
		t.Fatalf("replacement outcome %+v", oc[acts[0].ID])
	}
	if len(w.mgr.Removals()) != 1 || len(w.client.Stops()) != 1 {
		t.Fatalf("repeated writes: %v %v", w.mgr.Removals(), w.client.Stops())
	}
}

func TestAutomaticDispatchDoesNotActWhenItMustNot(t *testing.T) {
	cases := map[string]struct {
		mode  string
		setup func(w *world)
	}{
		"observe mode":  {"observe", func(w *world) { w.stalled(hashA, 1, 1) }},
		"paused mode":   {"paused", func(w *world) { w.stalled(hashA, 1, 1) }},
		"slow progress": {"automatic", func(w *world) { w.stalled(hashA, 1, 1) }},
		"user paused": {"automatic", func(w *world) {
			w.stalled(hashA, 1, 1)
			w.client.Update(hashA, func(tr *domain.Transfer) { tr.StateClass, tr.NativeState = domain.StatePaused, "stoppedDL" })
		}},
		"queued": {"automatic", func(w *world) {
			w.stalled(hashA, 1, 1)
			w.client.Update(hashA, func(tr *domain.Transfer) { tr.StateClass, tr.NativeState = domain.StateQueued, "queuedDL" })
		}},
		"unmonitored content": {"automatic", func(w *world) {
			w.stalled(hashA, 1, 1)
			w.mgr.Unmonitor("episode:101")
		}},
		"manager down": {"automatic", func(w *world) {
			w.stalled(hashA, 1, 1)
			w.mgr.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
		}},
		"correlated stalls on one client": {"automatic", func(w *world) {
			w.stalled(hashA, 1, 1)
			w.stalled(hashB, 2, 2)
			w.stalled(hashC, 3, 3)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, tc.mode, 20)
			tc.setup(w)
			if name == "slow progress" {
				for i := 0; i < 24; i++ {
					w.client.Update(hashA, func(tr *domain.Transfer) {
						v, _ := tr.VerifiedBytes.Get()
						tr.VerifiedBytes = domain.KnownValue(v + 1<<20)
					})
					w.run(10 * time.Minute)
				}
			} else {
				w.run(4 * time.Hour)
			}
			if len(w.mgr.Removals())+len(w.client.Stops()) != 0 {
				t.Fatalf("wrote: %v %v", w.mgr.Removals(), w.client.Stops())
			}
			switch name {
			case "slow progress", "user paused", "queued":
				// Not merely refused later: never diagnosed as a stall.
				if fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: domain.FindingTransferStalled}); len(fs) != 0 {
					t.Fatalf("diagnosed: %+v", fs[0])
				}
			case "observe mode", "paused mode":
				if d, _ := w.svc.Finding(context.Background(), w.finding(hashA).ID); d.Automation != nil {
					t.Fatalf("evaluated outside automatic mode: %+v", d.Automation)
				}
			}
			if name == "correlated stalls on one client" || name == "unmonitored content" {
				d, _ := w.svc.Finding(context.Background(), w.finding(hashA).ID)
				want := map[string]string{"correlated stalls on one client": store.EvalHeld, "unmonitored content": store.EvalRefused}[name]
				if d.Automation == nil || d.Automation.Result != want {
					t.Fatalf("automation %+v, want %s", d.Automation, want)
				}
			}
		})
	}
}

// Pacing defers eligible work with a visible time, then resumes it
// oldest-first when the allowance returns.
func TestPacingDefersAndResumesOldestFirst(t *testing.T) {
	w := newWorld(t, "automatic", 1)
	w.stalled(hashA, 1, 1)
	w.run(30 * time.Minute)
	w.stalled(hashB, 2, 2)
	w.run(4 * time.Hour)
	stops := w.client.Stops()
	if len(stops) != 2 || stops[0] != hashA || stops[1] != hashB {
		t.Fatalf("stops %v: want the older finding first, then the deferred one", stops)
	}
	// While deferred, the finding said when it would be eligible again.
	evs, _, _ := w.st.Attempts(context.Background(), "pacing:global", 0)
	if len(evs) != 2 || evs[0].At.Sub(evs[1].At) < time.Hour {
		t.Fatalf("pacing history %+v: the second action did not wait for the window", evs)
	}
}

// An accepted request whose effect is never observed is held, holds its
// scope, and is never re-sent by later cycles.
func TestUncertainOutcomeIsNotResentAndHoldsTheScope(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.mgr.FailRemovals(&adapter.Error{Class: adapter.ErrUncertain, Op: "fake.queue_remove", Message: "timeout awaiting response"}, true)
	w.stalled(hashA, 1, 1)
	w.stalled(hashD, 4, 4)
	w.run(5 * time.Hour)
	if len(w.mgr.Removals()) != 1 || len(w.client.Stops()) != 0 {
		t.Fatalf("removals %v stops %v", w.mgr.Removals(), w.client.Stops())
	}
	acts, _ := w.svc.Actions(context.Background(), 10)
	if len(acts) != 1 || acts[0].State != store.IntentUncertain {
		t.Fatalf("actions %+v", acts)
	}
	other := w.finding(hashD)
	if strings.Contains(string(acts[0].Subject), hashD) {
		other = w.finding(hashA)
	}
	d, _ := w.svc.Finding(context.Background(), other.ID)
	if d.Automation == nil || d.Automation.Result != store.EvalHeld || !strings.Contains(d.Automation.Detail, "needs review") {
		t.Fatalf("other finding not held by the uncertain action: %+v", d.Automation)
	}
}

// An unreadable manager holds only work that depends on it: a manager
// configured for another client cannot own this client's transfers.
// (Managers sharing one client fail closed on each other's outage, because
// the unreadable one could own the transfer.)
func TestManagerOutageBlocksOnlyItsScope(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.stalled(hashA, 1, 1)
	w.client2.SetTransfers(fake.Transfer(hashB, "Film.2020", domain.StateDownloading, 2*gib, 10*gib, t0))
	w.radarr.SetQueue(fake.Movie(1, hashB, "Film.2020", "Film", 2020))
	w.run(30 * time.Minute)
	w.radarr.Fail(&adapter.Error{Class: adapter.ErrTransient, Op: "fake", Message: "down"})
	w.run(4 * time.Hour)
	if s := w.client.Stops(); len(s) != 1 || s[0] != hashA {
		t.Fatalf("stops %v: want the Sonarr download despite the Radarr outage", s)
	}
	if len(w.radarr.Removals()) != 0 || len(w.client2.Stops()) != 0 {
		t.Fatalf("acted on Radarr's download while Radarr was unreadable: %v %v", w.radarr.Removals(), w.client2.Stops())
	}
	// Once Radarr answers again, its download is acted on too.
	w.radarr.Fail(nil)
	w.run(30 * time.Minute)
	if len(w.radarr.Removals()) != 1 || len(w.client2.Stops()) != 1 {
		t.Fatalf("after recovery: radarr %v stops %v", w.radarr.Removals(), w.client2.Stops())
	}
}

// Persistently refused findings must not starve a newer eligible one.
func TestRefusedFindingsDoNotStarveOthers(t *testing.T) {
	w := newWorld(t, "automatic", -1)
	for ep := int64(1); ep <= 5; ep++ {
		h := strings.Repeat(string(rune('0'+ep)), 40)
		w.stalled(h, ep, ep)
		w.mgr.Unmonitor("episode:" + itoa(int(100+ep)))
	}
	w.run(3 * time.Hour)
	if len(w.mgr.Removals()) != 0 {
		t.Fatalf("acted on unmonitored content: %v", w.mgr.Removals())
	}
	w.stalled(hashA, 6, 6)
	w.run(3 * time.Hour)
	if s := w.client.Stops(); len(s) != 1 || s[0] != hashA {
		t.Fatalf("stops %v: the eligible finding was starved by refused ones", s)
	}
}

// A workflow pause stops automatic and manual actions alike, is checked
// again before every write (an earlier preview does not get past it), and
// keeps the finding's decision visible; resuming lets automation proceed.
func TestWorkflowPauseStopsAutomaticAndManualActions(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, "automatic", 20)
	if err := w.svc.PauseWorkflow(ctx, policy.WorkflowNoProgressRecovery, app.ActorCLI, ""); err == nil {
		t.Fatal("pause without a note accepted")
	}
	if err := w.svc.PauseWorkflow(ctx, policy.WorkflowNoProgressRecovery, app.ActorCLI, "tracker maintenance tonight"); err != nil {
		t.Fatal(err)
	}
	w.stalled(hashA, 1, 1)
	w.run(4 * time.Hour)
	if len(w.mgr.Removals()) != 0 || len(w.client.Stops()) != 0 {
		t.Fatalf("paused workflow sent writes: %v %v", w.mgr.Removals(), w.client.Stops())
	}
	f := w.finding(hashA)
	d, _ := w.svc.Finding(ctx, f.ID)
	if d.Automation == nil || d.Automation.Result != store.EvalNotPermitted || !strings.Contains(d.Automation.Detail, "tracker maintenance tonight") {
		t.Fatalf("finding automation %+v", d.Automation)
	}
	// Manual: refused, with the pause named, and nothing sent.
	if _, _, err := w.svc.Act(ctx, f.ID, app.ActorCLI); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("manual action while paused: %v", err)
	}
	// Resume: the preview is eligible again.
	if err := w.svc.ResumeWorkflow(ctx, policy.WorkflowNoProgressRecovery, app.ActorCLI, "maintenance over"); err != nil {
		t.Fatal(err)
	}
	pv, err := w.svc.Preview(ctx, f.ID, action.OriginManual)
	if err != nil || pv.Proposal == nil || !pv.Proposal.Eligible() {
		t.Fatalf("preview after resume: %+v %v", pv, err)
	}
	if err := w.svc.PauseWorkflow(ctx, policy.WorkflowNoProgressRecovery, app.ActorWeb, "again"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.ResumeWorkflow(ctx, policy.WorkflowNoProgressRecovery, app.ActorWeb, "done"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.ResumeWorkflow(ctx, policy.WorkflowNoProgressRecovery, app.ActorWeb, "done"); err == nil {
		t.Fatal("resuming a workflow that is not paused succeeded")
	}
	w.run(time.Hour)
	if len(w.mgr.Removals()) != 1 || len(w.client.Stops()) != 1 {
		t.Fatalf("after resume: %v %v", w.mgr.Removals(), w.client.Stops())
	}
}

// R8: a manual action must not execute a diagnosis made under older, less
// restrictive diagnostic settings. A new process (headless CLI after a
// configuration edit) loads the stricter policy without an engine cycle.
func TestManualActionRequiresDiagnosisUnderCurrentSettings(t *testing.T) {
	w := newWorld(t, "paused", 20)
	w.stalled(hashA, 1, 1)
	w.run(3 * time.Hour)
	f := w.finding(hashA)
	src := strings.NewReplacer("%MODE%", "paused", "%ACQ%", "20", "%CORR%", "3").Replace(autoConfig)
	cfg, _, err := config.Parse([]byte(src+"\n[policy]\nno_progress_window = \"24h\"\n"), "/x/housekeeparr.toml")
	if err != nil {
		t.Fatal(err)
	}
	svc := app.New(w.st, cfg, w.clock.Now, nil)
	ex := &okExec{}
	svc.SetExecutor(ex)
	pv, err := svc.Preview(context.Background(), f.ID, action.OriginManual)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Proposal.Eligible() {
		t.Fatalf("a 24h policy accepted a diagnosis accumulated over 3h")
	}
	found := false
	for _, b := range pv.Proposal.Blockers() {
		if b.Name == "diagnosis under current policy" && strings.Contains(b.Detail, "re-evaluated") {
			found = true
		}
	}
	if !found {
		t.Fatalf("blockers do not explain re-evaluation: %+v", pv.Proposal.Blockers())
	}
	_, res, _ := svc.Act(context.Background(), f.ID, app.ActorCLI)
	if res != nil && res.State == store.IntentSucceeded || len(ex.sent) > 0 {
		t.Fatalf("manual action executed under changed timing: sent=%v", ex.sent)
	}
	if got := w.finding(hashA); got.State != domain.FindingOpen {
		t.Fatalf("finding changed to %s; it must stay open pending re-evaluation", got.State)
	}
}

// R8 with scoped policy: the planner resolves the same transfer-scoped
// diagnostic settings as the engine, so a diagnosis under an override is
// current for dispatch; editing that override requires re-evaluation.
func TestScopedSettingsDiagnosisIsCurrentUntilEdited(t *testing.T) {
	override := func(window string) string {
		return "\n[[override]]\ntransfer = \"qbit|" + hashA + "\"\nreason = \"test\"\n[override.policy]\nnew_download_grace = \"0s\"\nno_progress_window = \"" + window + "\"\n"
	}
	w := newWorldWith(t, "automatic", 20, override("30m"))
	w.stalled(hashA, 1, 1)
	w.stalled(hashB, 2, 2)
	w.run(time.Hour)
	if s := w.client.Stops(); len(s) != 1 || s[0] != hashA {
		t.Fatalf("scoped diagnosis was not dispatched: stops %v", s)
	}

	p := newWorldWith(t, "paused", 20, override("30m"))
	p.stalled(hashA, 1, 1)
	p.run(time.Hour)
	f := p.finding(hashA)
	reload := func(extra string) *app.Service {
		src := strings.NewReplacer("%MODE%", "paused", "%ACQ%", "20", "%CORR%", "3").Replace(autoConfig) + extra
		cfg, _, err := config.Parse([]byte(src), "/x/housekeeparr.toml")
		if err != nil {
			t.Fatal(err)
		}
		return app.New(p.st, cfg, p.clock.Now, nil)
	}
	blocked := func(svc *app.Service) bool {
		pv, err := svc.Preview(context.Background(), f.ID, action.OriginManual)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range pv.Proposal.Blockers() {
			if b.Name == "diagnosis under current policy" {
				return true
			}
		}
		return false
	}
	if blocked(reload(override("30m") + "\n[[override]]\ntransfer = \"qbit|" + hashC + "\"\nreason = \"other\"\n[override.policy]\nno_progress_window = \"24h\"\n")) {
		t.Fatal("an override for another transfer invalidated this diagnosis")
	}
	if !blocked(reload(override("24h"))) {
		t.Fatal("editing the transfer's override kept the older diagnosis usable")
	}
}

const magnetRule = `
[[rule]]
name = "public magnets"
priority = 1
[rule.match]
client = "qbit"
privacy = "unknown"
[rule.automation]
metadata_recovery = true
`

// Metadata recovery runs through the same engine → dispatcher → coordinator
// path, with its own diagnosis, permission and capability gates. Privacy is
// always unknown before metadata; with no payload downloaded that is
// accepted by default, and a scope can refuse it.
func TestAutomaticMetadataRecoveryEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra string
		act   bool
	}{
		{"recovered by default", magnetRule, true},
		{"refused where the scope turned unknown privacy off", magnetRule + "[rule.unknown_privacy]\nmetadata_recovery = false\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorldWith(t, "automatic", 20, tc.extra)
			action.Tested = append(action.Tested, action.TestedWorkflow{Workflow: policy.WorkflowMetadataRecovery, Manager: adapter.KindSonarr,
				ManagerVersion: "0.0.0-fake", Client: adapter.KindQBittorrent, ClientVersion: "0.0.0-fake"})
			pr, _ := w.client.Probe(context.Background())
			pr.Read[adapter.CapMetadataState] = adapter.CapabilityStatus{Support: adapter.Supported}
			w.client.SetProbe(pr)
			w.client.SetTransfers(fake.Transfer(hashA, "Show.S01E01", domain.StateMetadata, 0, 0, t0))
			// As observed with rTorrent: without metadata the queue row is
			// titled after the client's placeholder name, while the manager
			// blocklists under the grabbed release's title.
			w.mgr.SetQueue(fake.Episode(1, hashA, strings.ToUpper(hashA)+".meta", "Show", 1, 1))
			w.mgr.AddGrab(hashA, "Show.S01E01.1080p.WEB-GRP")
			w.run(3 * time.Hour)
			fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: domain.FindingMetadataTimeout, ClientID: "qbit", NativeID: hashA})
			if len(fs) != 1 {
				t.Fatalf("metadata findings %+v", fs)
			}
			stops, removals := w.client.Stops(), w.mgr.Removals()
			if !tc.act {
				if len(stops) != 0 || len(removals) != 0 {
					t.Fatalf("acted where unknown privacy is refused: %v %v", stops, removals)
				}
				d, _ := w.svc.Finding(context.Background(), fs[0].ID)
				if d.Automation == nil || !strings.Contains(d.Automation.Detail, "unknown_privacy.metadata_recovery") {
					t.Fatalf("refusal not explained: %+v", d.Automation)
				}
				return
			}
			if len(stops) != 1 || stops[0] != hashA || len(removals) != 1 || removals[0] != (arr.RemoveFlags{Blocklist: true}) {
				t.Fatalf("stops %v removals %+v", stops, removals)
			}
			acts, _ := w.svc.Actions(context.Background(), 10)
			if len(acts) != 1 || acts[0].Workflow != string(policy.WorkflowMetadataRecovery) || acts[0].State != store.IntentSucceeded {
				t.Fatalf("actions %+v", acts)
			}
			if ids, _ := w.mgr.BlocklistEntries(context.Background(), arr.BlocklistMatch{SourceTitle: "Show.S01E01.1080p.WEB-GRP"}); len(ids) != 1 {
				t.Fatalf("blocklist entries under the grabbed title: %v", ids)
			}
			ret, _ := w.st.RetainedTransfers(context.Background(), false, 0)
			// No payload, but the logical size is not invented as zero.
			if len(ret) != 1 || ret[0].WantedBytes != nil || ret[0].VerifiedBytes == nil || *ret[0].VerifiedBytes != 0 || ret[0].Disposition != store.RetainedStopped {
				t.Fatalf("retained %+v (want unknown size, zero payload, stopped)", ret)
			}
			if u, _ := w.st.RetainedUsage(context.Background()); u.Count != 1 || u.NoPayload != 1 || u.UnknownSize != 0 || u.KnownBytes != 0 {
				t.Fatalf("usage %+v", u)
			}
			// Resumed by the operator, it obtains metadata: its size is
			// then accounted for from the client's listing.
			w.client.Update(hashA, func(tr *domain.Transfer) {
				tr.StateClass, tr.WantedBytes, tr.VerifiedBytes = domain.StateDownloading, domain.KnownValue(3*gib), domain.KnownValue(gib)
			})
			w.run(10 * time.Minute)
			if u, _ := w.st.RetainedUsage(context.Background()); u.Count != 1 || u.NoPayload != 0 || u.KnownBytes != 3*gib {
				t.Fatalf("usage after metadata arrived %+v", u)
			}
		})
	}
}

// A stalled private torrent, end to end: by default its grab is failed and
// blocklisted (so the manager fetches a replacement) and the torrent is
// never stopped; "stop" also stops it; "observe" sends nothing.
func TestPrivateTorrentRecoveryEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		mode              string
		removals, stopped int
	}{{"", 1, 0}, {"stop", 1, 1}, {"observe", 0, 0}} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			extra := ""
			if tc.mode != "" {
				extra = "\n[[rule]]\nname = \"private\"\npriority = 1\nprivate_recovery = \"" + tc.mode + "\"\n[rule.match]\nclient = \"qbit\"\n"
			}
			w := newWorldWith(t, "automatic", 20, extra)
			w.stalled(hashA, 1, 1)
			w.client.Update(hashA, func(tr *domain.Transfer) { tr.Private = domain.KnownValue(true) })
			w.run(4 * time.Hour)
			if r, s := w.mgr.Removals(), w.client.Stops(); len(r) != tc.removals || len(s) != tc.stopped {
				t.Fatalf("removals %+v stops %v", r, s)
			}
			acts, _ := w.svc.Actions(context.Background(), 10)
			if tc.removals == 0 {
				if len(acts) != 0 {
					t.Fatalf("actions %+v", acts)
				}
				return
			}
			if len(acts) != 1 || acts[0].State != store.IntentSucceeded {
				t.Fatalf("actions %+v", acts)
			}
			if r := w.mgr.Removals()[0]; r != (arr.RemoveFlags{Blocklist: true}) {
				t.Fatalf("flags %+v (removeFromClient must stay false)", r)
			}
			// Either way the torrent stays in the client with its data and
			// counts as retained: stopped, or left running.
			ret, _ := w.st.RetainedTransfers(context.Background(), false, 0)
			wantDisp := store.RetainedRunning
			if tc.stopped == 1 {
				wantDisp = store.RetainedStopped
			}
			if len(ret) != 1 || ret[0].Disposition != wantDisp {
				t.Fatalf("retained %+v", ret)
			}
			ts, _ := w.client.ListTransfers(context.Background())
			if tc.stopped == 0 && ts.Transfers[0].StateClass != domain.StateDownloading {
				t.Fatalf("torrent no longer running: %s", ts.Transfers[0].StateClass)
			}
		})
	}
}

// Stalls an earlier recovery already handled (private torrents left
// running) neither trigger nor dilute the correlated-stall hold, and their
// findings explain what was done; stalls that were never managed still
// count, so a shared outage holds recovery.
func TestCorrelatedHoldIgnoresAlreadyRecoveredStalls(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	for i, h := range []string{hashA, hashB} {
		w.stalled(h, int64(i+1), int64(i+1))
		w.client.Update(h, func(tr *domain.Transfer) { tr.Private = domain.KnownValue(true) })
		w.run(4 * time.Hour) // one at a time, so they are not themselves correlated
	}
	if r, s := w.mgr.Removals(), w.client.Stops(); len(r) != 2 || len(s) != 0 {
		t.Fatalf("setup: removals %d stops %v", len(r), s)
	}
	pv, err := w.svc.Preview(context.Background(), w.finding(hashA).ID, action.OriginManual)
	if err != nil {
		t.Fatal(err)
	}
	explained := false
	for _, b := range pv.Proposal.Blockers() {
		explained = explained || b.Name == "one manager association" && strings.Contains(b.Detail, "already failed and blocklisted") && strings.Contains(b.Detail, "keep_running")
	}
	if !explained {
		t.Fatalf("a torrent left running is not explained: %+v", pv.Proposal.Blockers())
	}
	// A third, public stall: 1 of 1 candidates, not "3 of 3 stalled together".
	w.stalled(hashC, 3, 3)
	w.run(4 * time.Hour)
	if s := w.client.Stops(); len(s) != 1 || s[0] != hashC {
		t.Fatalf("recovery held by torrents that were already handled: stops %v", s)
	}
}

func TestCorrelatedHoldCountsUnmanagedStalls(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.stalled(hashA, 1, 1)
	ts, _ := w.client.ListTransfers(context.Background())
	// Three more stalled downloads no manager knows about: 4 of 4 stalled.
	all := ts.Transfers
	for _, h := range []string{hashB, hashC, hashD} {
		all = append(all, fake.Transfer(h, "Manual."+h[:4], domain.StateDownloading, 2*gib, 10*gib, t0))
	}
	w.client.SetTransfers(all...)
	w.run(4 * time.Hour)
	if r := w.mgr.Removals(); len(r) != 0 {
		t.Fatalf("a shared stall (managed and unmanaged together) did not hold recovery: %+v", r)
	}
}

// A stalled torrent whose privacy the client cannot report is treated as
// private by default (blocklisted, never stopped); a scope can leave such
// torrents alone.
func TestUnknownPrivacyTorrentRecoveryEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name, extra string
		removals    int
	}{
		{"treated as private by default", "", 1},
		{"left alone where turned off", "\n[[rule]]\nname = \"cautious\"\npriority = 1\n[rule.match]\nclient = \"qbit\"\n[rule.unknown_privacy]\nno_progress_recovery = false\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorldWith(t, "automatic", 20, tc.extra)
			w.stalled(hashA, 1, 1)
			w.client.Update(hashA, func(tr *domain.Transfer) {
				tr.Private = domain.UnknownValue[bool]("not reported by this client version")
			})
			w.run(4 * time.Hour)
			if r, s := w.mgr.Removals(), w.client.Stops(); len(r) != tc.removals || len(s) != 0 {
				t.Fatalf("removals %+v stops %v", r, s)
			}
		})
	}
}

// R11: a recovery exempts only the physical transfer it handled (client
// plus hash). The same hash on another client, in a shared outage there,
// still counts: three simultaneous stalls on qbit2 hold recovery.
func TestRecoveredHashDoesNotHideOtherClientOutage(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.stalled(hashA, 1, 1)
	w.run(4 * time.Hour)
	if len(w.mgr.Removals()) != 1 {
		t.Fatal("setup recovery failed")
	}
	w.client2.SetTransfers(
		fake.Transfer(hashA, "Film.2020", domain.StateDownloading, 2*gib, 10*gib, t0),
		fake.Transfer(hashB, "Manual.B", domain.StateDownloading, 2*gib, 10*gib, t0),
		fake.Transfer(hashC, "Manual.C", domain.StateDownloading, 2*gib, 10*gib, t0))
	w.radarr.SetQueue(fake.Movie(1, hashA, "Film.2020", "Film", 2020))
	w.run(4 * time.Hour)
	if n := len(w.radarr.Removals()); n != 0 {
		t.Fatalf("shared outage on qbit2 should hold all 3 stalls; sent %d manager writes after the same hash was recovered on qbit", n)
	}
}

// R11: a transfer a recovery left running is exempt only while no manager
// associates it again. Once the same release is grabbed again it is a
// candidate, so with two other stalls it is part of a 3-of-3 shared stall.
func TestReassociatedRecoveredTransferCountsTowardHold(t *testing.T) {
	w := newWorld(t, "automatic", 20)
	w.stalled(hashA, 1, 1)
	w.client.Update(hashA, func(tr *domain.Transfer) { tr.Private = domain.KnownValue(true) })
	w.run(4 * time.Hour)
	if r, s := w.mgr.Removals(), w.client.Stops(); len(r) != 1 || len(s) != 0 {
		t.Fatalf("setup: removals %d stops %v", len(r), s)
	}
	ret, _ := w.st.RetainedTransfers(context.Background(), false, 0)
	if len(ret) != 1 || ret[0].Disposition != store.RetainedRunning || ret[0].ClientID != "qbit" {
		t.Fatalf("a torrent left running is not retained as running: %+v", ret)
	}
	// The manager grabs the same release again (same episode, so its
	// content cooldown applies), and two other downloads stall with it.
	snap, _ := w.mgr.Queue(context.Background())
	w.mgr.SetQueue(append(snap.Items, fake.Episode(9, hashA, "Show.S01E01", "Show", 1, 1))...)
	w.stalled(hashB, 2, 2)
	w.stalled(hashC, 3, 3)
	w.run(4 * time.Hour)
	if r := w.mgr.Removals(); len(r) != 1 {
		t.Fatalf("3 of 3 downloads stalled together on qbit, yet recovery dispatched: %+v", r)
	}
}

const preservedRule = "\n[[rule]]\nname = \"preserved\"\npriority = 1\n[rule.automation]\npreserved_data_failure = true\n[rule.match]\nclient = \"qbit\"\n"

// SPEC §7.2 end to end with simulated applications: a public download its
// tracker rejects as not registered is diagnosed after the confirmation
// window (before the no-progress window) and handled once: the grab is
// failed and blocklisted with every flag explicit, and the torrent stopped
// and kept. Other tracker errors, private torrents, and local errors are
// never acted on.
func TestPreservedDataFailureEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mutate          func(*domain.Transfer)
		removals, stops int
		findingType     string
	}{
		{"unregistered", func(tr *domain.Transfer) {
			tr.SourceFailure, tr.ClientMessage = domain.KnownValue(domain.SourceUnregistered), domain.KnownValue("Unregistered torrent")
		}, 1, 1, domain.FindingSourceRejected},
		{"other tracker error", func(tr *domain.Transfer) {
			tr.SourceFailure, tr.ClientMessage = domain.KnownValue(domain.SourceTrackerError), domain.KnownValue("Passkey not found")
		}, 0, 0, ""},
		// Private: private_recovery's default keep_running (blocklist, never stop).
		{"private", func(tr *domain.Transfer) {
			tr.SourceFailure, tr.Private = domain.KnownValue(domain.SourceUnregistered), domain.KnownValue(true)
		}, 1, 0, domain.FindingSourceRejected},
		// Unknown privacy is treated as private: blocklisted, never stopped.
		{"unknown privacy", func(tr *domain.Transfer) {
			tr.SourceFailure, tr.Private = domain.KnownValue(domain.SourceUnregistered), domain.UnknownValue[bool]("not reported")
		}, 1, 0, domain.FindingSourceRejected},
		{"local error", func(tr *domain.Transfer) {
			tr.StateClass, tr.NativeState = domain.StateError, "local-error"
			tr.SourceFailure = domain.UnknownValue[domain.SourceFailure]("a local error hides any tracker result")
		}, 0, 0, domain.FindingTransferError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorldWith(t, "automatic", 20, preservedRule)
			action.Tested = append(action.Tested,
				action.TestedWorkflow{Workflow: policy.WorkflowPreservedDataFailure, Manager: adapter.KindSonarr, ManagerVersion: "0.0.0-fake", Client: adapter.KindQBittorrent, ClientVersion: "0.0.0-fake"})
			w.stalled(hashA, 1, 1)
			w.client.Update(hashA, tc.mutate)
			// Grace (30 min) plus confirmation (30 min), well before the
			// 90-minute no-progress window ends; no_progress_recovery is
			// enabled too, so only the source diagnosis can act this early.
			w.run(70 * time.Minute)
			if r, s := w.mgr.Removals(), w.client.Stops(); len(r) != tc.removals || len(s) != tc.stops {
				t.Fatalf("removals %+v stops %v", r, s)
			}
			if tc.removals > 0 {
				if r := w.mgr.Removals()[0]; r != (arr.RemoveFlags{Blocklist: true}) {
					t.Fatalf("flags %+v (removeFromClient and skipRedownload must stay false)", r)
				}
				acts, _ := w.svc.Actions(context.Background(), 10)
				if len(acts) != 1 || acts[0].Workflow != string(policy.WorkflowPreservedDataFailure) || acts[0].State != store.IntentSucceeded {
					t.Fatalf("actions %+v", acts)
				}
				want := store.RetainedStopped
				if tc.stops == 0 {
					want = store.RetainedRunning
				}
				ret, _ := w.st.RetainedTransfers(context.Background(), false, 0)
				if len(ret) != 1 || ret[0].Disposition != want {
					t.Fatalf("retained %+v", ret)
				}
			}
			if tc.findingType == "" {
				return
			}
			fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: tc.findingType, ClientID: "qbit", NativeID: hashA})
			if len(fs) == 0 {
				t.Fatalf("no %s finding", tc.findingType)
			}
			pv, err := w.svc.Preview(context.Background(), fs[0].ID, action.OriginManual)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "local error":
				if pv.Proposal != nil || !strings.Contains(pv.NoAction, "diagnosis only") {
					t.Fatalf("local error has an action: %+v", pv)
				}
			}
		})
	}
}

func slicesContainsBlocker(p *action.Proposal, name string) bool {
	for _, b := range p.Blockers() {
		if b.Name == name {
			return true
		}
	}
	return false
}

// A diagnosed finding is queued for each configured notification target in
// the cycle that opens it, once; observe mode still notifies.
func TestCycleQueuesNotifications(t *testing.T) {
	w := newWorldWith(t, "observe", 20, "\n[[notify]]\nid = \"hook\"\nurl = \"env:HK_TEST_HOOK\"\n")
	w.stalled(hashA, 1, 1)
	w.run(4 * time.Hour)
	ns, err := w.st.RecentNotifications(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 1 || ns[0].Kind != "finding.opened" || ns[0].Target != "hook" || ns[0].FindingID == nil || *ns[0].FindingID != w.finding(hashA).ID {
		t.Fatalf("notifications %+v", ns)
	}
}

// An automatic recovery notifies its outcome once; exhausted attempts
// notify the user that review is needed.
func TestAutomationOutcomesAreNotified(t *testing.T) {
	w := newWorldWith(t, "automatic", 20, "\n[[notify]]\nid = \"hook\"\nurl = \"env:HK_TEST_HOOK\"\nmin_severity = \"notice\"\n")
	w.stalled(hashA, 1, 1)
	w.run(4 * time.Hour)
	ns, _ := w.st.RecentNotifications(context.Background(), 20)
	var kinds []string
	actions := 0
	for _, n := range ns {
		kinds = append(kinds, n.Kind+":"+n.Title)
		if n.Kind == "action.outcome" {
			actions++
			if n.Severity != "notice" || !strings.HasPrefix(n.Title, "Grab failed and replacement requested") {
				t.Errorf("action notice %+v", n)
			}
		}
	}
	if actions != 1 {
		t.Fatalf("want one action notification, got %v", kinds)
	}
}

const missingRule = "\n[[rule]]\nname = \"searches\"\npriority = 1\n[rule.automation]\nmissing_search = true\n[rule.match]\nmanager = \"sonarr\"\n"

// Missing lists are read only while proactive searches could be enabled,
// at most hourly per manager, and only a complete read marks items gone.
func TestMissingListsAreReadBoundedly(t *testing.T) {
	w := newWorld(t, "observe", 20)
	w.mgr.SetMissing(fake.MissingEpisode(11, 3, "Show S01E11", t0.Add(-48*time.Hour)))
	w.run(time.Hour)
	if n := w.mgr.MissingReads(); n != 0 {
		t.Fatalf("read %d times with missing_search enabled nowhere", n)
	}
	w = newWorldWith(t, "observe", 20, missingRule)
	w.mgr.SetMissing(fake.MissingEpisode(11, 3, "Show S01E11", t0.Add(-48*time.Hour)), fake.MissingEpisode(12, 3, "Show S01E12", t0.Add(-24*time.Hour)))
	w.run(2 * time.Hour)
	if n := w.mgr.MissingReads(); n != 2 {
		t.Fatalf("12 cycles over 2h read the list %d times, want 2 (hourly)", n)
	}
	list, _ := w.st.MissingContent(context.Background(), store.MissingFilter{PresentOnly: true})
	if len(list) != 2 {
		t.Fatalf("stored %+v", list)
	}
	w.mgr.PartialMissing(true)
	w.mgr.SetMissing(fake.MissingEpisode(12, 3, "Show S01E12", t0.Add(-24*time.Hour)))
	w.run(time.Hour)
	if list, _ := w.st.MissingContent(context.Background(), store.MissingFilter{PresentOnly: true}); len(list) != 2 {
		t.Fatalf("an incomplete read removed items: %+v", list)
	}
	w.mgr.PartialMissing(false)
	w.run(time.Hour)
	if list, _ := w.st.MissingContent(context.Background(), store.MissingFilter{PresentOnly: true}); len(list) != 1 || list[0].ContentKey != "episode:12" {
		t.Fatalf("after a complete read: %+v", list)
	}
}

// runSearches cycles like run, then runs the proactive-search pass as the
// worker does.
func (w *world) runSearches(d time.Duration) []app.SearchReport {
	w.t.Helper()
	var reps []app.SearchReport
	for end := w.clock.Now().Add(d); w.clock.Now().Before(end); {
		w.clock.Advance(10 * time.Minute)
		rep := w.eng.Cycle(context.Background(), nil)
		if rep.StoreError != "" {
			w.t.Fatal(rep.StoreError)
		}
		if _, err := w.svc.DispatchAutomatic(context.Background(), rep.Fresh()); err != nil {
			w.t.Fatal(err)
		}
		r, err := w.svc.DispatchSearches(context.Background(), rep.Fresh())
		if err != nil {
			w.t.Fatal(err)
		}
		reps = append(reps, r)
	}
	return reps
}

func searchWorld(t *testing.T, mode string) *world {
	w := newWorldWith(t, mode, 20, missingRule)
	action.Tested = append(action.Tested, action.TestedWorkflow{Workflow: policy.WorkflowMissingSearch, Manager: adapter.KindSonarr, ManagerVersion: "0.0.0-fake"})
	return w
}

// SPEC §9.1 with simulated applications: one search at a time for a
// monitored, aired, missing episode that nothing is downloading; a search
// that leaves it missing backs off; queued content is never searched; and
// nothing is sent outside automatic mode.
func TestProactiveSearchEndToEnd(t *testing.T) {
	w := searchWorld(t, "automatic")
	w.mgr.SetMissing(fake.MissingEpisode(101, 3, "Show S01E01", t0.Add(-72*time.Hour)), fake.MissingEpisode(102, 3, "Show S01E02", t0.Add(-48*time.Hour)))
	// Episode 101 is already queued (being downloaded): never searched.
	// A queue row for episode 101 (fake IDs are season*100+episode) whose
	// transfer is on no client, so nothing else acts on it.
	w.mgr.SetQueue(fake.Episode(1, hashD, "Show.S01E01", "Show", 1, 1))
	w.runSearches(time.Hour)
	if s := w.mgr.Searches(); len(s) != 1 || s[0][0] != 102 {
		t.Fatalf("searches %v (want exactly episode 102)", s)
	}
	it, _ := w.st.MissingRecord(context.Background(), "sonarr", "episode:102")
	// The next pass followed the command to its end: nothing downloaded.
	if it.State != store.MissingNoResult || it.Searches != 1 || it.NextSearchAt == nil || it.NextSearchAt.Sub(*it.LastSearchAt) != 24*time.Hour ||
		!strings.Contains(it.Note, "not evidence that no release exists") {
		t.Fatalf("search state %+v", it)
	}
	// Still missing a few hours later: backing off, nothing more sent.
	w.runSearches(6 * time.Hour)
	if s := w.mgr.Searches(); len(s) != 1 {
		t.Fatalf("searched again during backoff: %v", s)
	}
	// After the backoff it is searched once more, then waits twice as long.
	w.runSearches(20 * time.Hour)
	it, _ = w.st.MissingRecord(context.Background(), "sonarr", "episode:102")
	if s := w.mgr.Searches(); len(s) != 2 || it.Searches != 2 || it.NextSearchAt.Sub(*it.LastSearchAt) != 48*time.Hour {
		t.Fatalf("after backoff: searches %v, state %+v", s, it)
	}
	acts, _ := w.svc.Actions(context.Background(), 10)
	n := 0
	for _, a := range acts {
		if a.Workflow == string(policy.WorkflowMissingSearch) {
			n++
			if a.State != store.IntentSucceeded {
				t.Fatalf("search action %+v", a)
			}
		}
	}
	if n != 2 {
		t.Fatalf("journaled searches %d", n)
	}
}

func TestProactiveSearchRefusals(t *testing.T) {
	t.Run("observe mode", func(t *testing.T) {
		w := searchWorld(t, "observe")
		w.mgr.SetMissing(fake.MissingEpisode(102, 3, "Show S01E02", t0.Add(-48*time.Hour)))
		w.runSearches(time.Hour)
		if s := w.mgr.Searches(); len(s) != 0 {
			t.Fatalf("searched in observe mode: %v", s)
		}
	})
	t.Run("not yet aired", func(t *testing.T) {
		w := searchWorld(t, "automatic")
		it := fake.MissingEpisode(103, 3, "Show S01E03", t0.Add(48*time.Hour))
		it.Available = domain.KnownValue(false)
		w.mgr.SetMissing(it)
		w.runSearches(time.Hour)
		if s := w.mgr.Searches(); len(s) != 0 {
			t.Fatalf("searched unavailable content: %v", s)
		}
	})
	t.Run("unmonitored by the time of the request", func(t *testing.T) {
		w := searchWorld(t, "automatic")
		w.mgr.SetMissing(fake.MissingEpisode(102, 3, "Show S01E02", t0.Add(-48*time.Hour)))
		w.mgr.Unmonitor("episode:102")
		reps := w.runSearches(30 * time.Minute)
		if s := w.mgr.Searches(); len(s) != 0 {
			t.Fatalf("searched unmonitored content: %v (%+v)", s, reps)
		}
	})
	t.Run("uncertain request is not re-sent", func(t *testing.T) {
		w := searchWorld(t, "automatic")
		w.mgr.SetMissing(fake.MissingEpisode(102, 3, "Show S01E02", t0.Add(-48*time.Hour)))
		w.mgr.FailSearches(&adapter.Error{Class: adapter.ErrUncertain, Op: "fake", Message: "timeout after send"})
		w.runSearches(3 * time.Hour)
		it, _ := w.st.MissingRecord(context.Background(), "sonarr", "episode:102")
		if it.State != store.MissingUncertain {
			t.Fatalf("state %+v", it)
		}
		acts, _ := w.svc.Actions(context.Background(), 10)
		if len(acts) != 1 || acts[0].State != store.IntentUncertain {
			t.Fatalf("actions %+v (one uncertain request, never repeated)", acts)
		}
	})
}

// SPEC §9.2: a torrent indexer the manager disabled after failures is a
// finding (incomplete coverage) that resolves only after two consecutive
// reads without it disabled; no automatic torrent indexer at all is a
// configuration finding.
func TestSourceFindings(t *testing.T) {
	w := newWorld(t, "observe", 20)
	until := t0.Add(6 * time.Hour)
	fail := t0
	w.mgr.SetSources(domain.Source{ID: 1, Name: "Tracker One", Protocol: "torrent", AutomaticSearch: true, StatusKnown: true, DisabledUntil: &until, LastFailure: &fail},
		domain.Source{ID: 2, Name: "News", Protocol: "usenet", AutomaticSearch: true, StatusKnown: true})
	w.run(10 * time.Minute)
	open := func(typ string) []domain.Finding {
		fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: typ})
		var out []domain.Finding
		for _, f := range fs {
			if f.State != domain.FindingResolved {
				out = append(out, f)
			}
		}
		return out
	}
	if fs := open("source.unavailable"); len(fs) != 1 || !strings.Contains(fs[0].Title, "Tracker One") {
		t.Fatalf("unavailable findings %+v", fs)
	}
	w.mgr.SetSources(domain.Source{ID: 1, Name: "Tracker One", Protocol: "torrent", AutomaticSearch: true, StatusKnown: true},
		domain.Source{ID: 2, Name: "News", Protocol: "usenet", AutomaticSearch: true, StatusKnown: true})
	w.run(10 * time.Minute)
	if fs := open("source.unavailable"); len(fs) != 1 {
		t.Fatalf("resolved after one good read: %+v", fs)
	}
	w.run(10 * time.Minute)
	if fs := open("source.unavailable"); len(fs) != 0 {
		t.Fatalf("not resolved after two good reads: %+v", fs)
	}
	w.mgr.SetSources(domain.Source{ID: 2, Name: "News", Protocol: "usenet", AutomaticSearch: true, StatusKnown: true})
	w.run(10 * time.Minute)
	if fs := open("source.none"); len(fs) != 1 {
		t.Fatalf("no torrent indexer: %+v", fs)
	}
}

// A search that found nothing while a torrent indexer was disabled says
// that coverage was incomplete, as possible impact rather than proof.
func TestSearchOutcomeNotesIncompleteCoverage(t *testing.T) {
	w := searchWorld(t, "automatic")
	until := t0.Add(48 * time.Hour)
	w.mgr.SetSources(domain.Source{ID: 1, Name: "Tracker One", Protocol: "torrent", AutomaticSearch: true, StatusKnown: true, DisabledUntil: &until},
		domain.Source{ID: 3, Name: "Tracker Two", Protocol: "torrent", AutomaticSearch: true, StatusKnown: true})
	w.mgr.SetMissing(fake.MissingEpisode(102, 3, "Show S01E02", t0.Add(-48*time.Hour)))
	w.runSearches(time.Hour)
	it, _ := w.st.MissingRecord(context.Background(), "sonarr", "episode:102")
	if it.State != store.MissingNoResult || !strings.Contains(it.Note, "coverage incomplete: 1 of 2 torrent indexers") || !strings.Contains(it.Note, "Tracker One") {
		t.Fatalf("%s: %s", it.State, it.Note)
	}
}

// A manager that does not report indexer status (Sonarr 4.0.20) produces
// no "unavailable" finding, and a search outcome says coverage could not
// be verified rather than that it was complete.
func TestUnknownSourceStatusIsNotHealthy(t *testing.T) {
	w := searchWorld(t, "automatic")
	w.mgr.SetSources(domain.Source{ID: 1, Name: "Tracker One", Protocol: "torrent", AutomaticSearch: true})
	w.mgr.SetMissing(fake.MissingEpisode(102, 3, "Show S01E02", t0.Add(-48*time.Hour)))
	w.runSearches(time.Hour)
	if fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: "source.unavailable"}); len(fs) != 0 {
		t.Fatalf("unknown status became a finding: %+v", fs)
	}
	it, _ := w.st.MissingRecord(context.Background(), "sonarr", "episode:102")
	if !strings.Contains(it.Note, "coverage could not be verified") {
		t.Fatalf("note %q", it.Note)
	}
	srcs, _ := w.st.Sources(context.Background(), "sonarr")
	if len(srcs) != 1 || srcs[0].OKStreak != 0 {
		t.Fatalf("an unknown status counted toward recovery: %+v", srcs)
	}
}

const packRule = "\n[[rule]]\nname = \"packs\"\npriority = 1\n[rule.automation]\npartial_pack = true\n[rule.match]\nmanager = \"sonarr\"\n"

// packWorld has a completed season pack on qbit whose Sonarr rows (E01-E03,
// IDs 201-203) are import-blocked with the partial-pack message, E01 and
// E02 imported from it (history and files), E03 neither.
func packWorld(t *testing.T, mutate func(w *world, rows []domain.QueueItem) []domain.QueueItem) *world {
	w := newWorldWith(t, "automatic", 20, packRule)
	action.Tested = append(action.Tested, action.TestedWorkflow{Workflow: policy.WorkflowPartialPack, Manager: adapter.KindSonarr, ManagerVersion: "0.0.0-fake",
		Client: adapter.KindQBittorrent, ClientVersion: "0.0.0-fake"})
	w.client.SetTransfers(fake.Transfer(hashA, "Show.S02", domain.StateComplete, 4*gib, 4*gib, t0))
	var rows []domain.QueueItem
	for i, ep := range []int64{1, 2, 3} {
		q := fake.Episode(int64(10+i), hashA, "Show.S02", "Show", 2, ep)
		q.TrackedState, q.TrackedStatus, q.Status = domain.KnownValue("importBlocked"), domain.KnownValue("warning"), "completed"
		q.StatusMessages = []domain.StatusMessage{{Title: domain.PartialPackMessage},
			{Title: "Show.S02E01.mkv", Messages: []string{domain.AlreadyImportedPrefix + "11/17/2026 14:50:03"}},
			{Title: "Show.S02E02.mkv", Messages: []string{domain.AlreadyImportedPrefix + "11/17/2026 14:50:03"}}}
		rows = append(rows, q)
	}
	w.mgr.AddImport(hashA, 201)
	w.mgr.AddImport(hashA, 202)
	w.mgr.SetHasFile("episode:201", true)
	w.mgr.SetHasFile("episode:202", true)
	w.mgr.SetHasFile("episode:203", false)
	if mutate != nil {
		rows = mutate(w, rows)
	}
	w.mgr.SetQueue(rows...)
	return w
}

// SPEC §8 / §13.4 scenario 4 with simulated applications: only the stale
// association is cleared, once, through the ignore path; nothing is sent to
// the client; uncertainty refuses.
func TestPartialPackEndToEnd(t *testing.T) {
	w := packWorld(t, nil)
	w.run(2 * time.Hour)
	r := w.mgr.Removals()
	if len(r) != 1 || r[0] != (arr.RemoveFlags{SkipRedownload: true}) {
		t.Fatalf("removals %+v (want exactly one ignore-path removal)", r)
	}
	if s := w.client.Stops(); len(s) != 0 {
		t.Fatalf("the client was touched: %v", s)
	}
	snap, _ := w.mgr.Queue(context.Background())
	if len(snap.Items) != 0 {
		t.Fatalf("rows left %+v", snap.Items)
	}
	acts, _ := w.svc.Actions(context.Background(), 10)
	if len(acts) != 1 || acts[0].Workflow != string(policy.WorkflowPartialPack) || acts[0].State != store.IntentSucceeded {
		t.Fatalf("actions %+v", acts)
	}
	fs, _ := w.st.Findings(context.Background(), store.FindingFilter{Type: domain.FindingPartialPack})
	if len(fs) != 1 || fs[0].State != domain.FindingResolved {
		t.Fatalf("finding %+v", fs)
	}
	// The association returning would be refused, not cleared again.
	for i, ep := range []int64{1, 2, 3} {
		q := fake.Episode(int64(20+i), hashA, "Show.S02", "Show", 2, ep)
		q.TrackedState, q.TrackedStatus, q.Status = domain.KnownValue("importBlocked"), domain.KnownValue("warning"), "completed"
		q.StatusMessages = []domain.StatusMessage{{Title: domain.PartialPackMessage}}
		snap.Items = append(snap.Items, q)
	}
	w.mgr.SetQueue(snap.Items...)
	w.run(2 * time.Hour)
	if r := w.mgr.Removals(); len(r) != 1 {
		t.Fatalf("cleared again after the association returned: %+v", r)
	}
}

func TestPartialPackUncertaintyRefuses(t *testing.T) {
	for name, mutate := range map[string]func(*world, []domain.QueueItem) []domain.QueueItem{
		"another import message": func(_ *world, rows []domain.QueueItem) []domain.QueueItem {
			for i := range rows {
				rows[i].StatusMessages = append(rows[i].StatusMessages, domain.StatusMessage{Title: "Show.S02E03.mkv", Messages: []string{"No audio tracks detected"}})
			}
			return rows
		},
		"no import record": func(w *world, rows []domain.QueueItem) []domain.QueueItem {
			w.mgr.SetHasFile("episode:202", true)
			w.mgr.SetHasFile("episode:203", true) // a file not imported from this download
			return rows
		},
		"nothing missing": func(w *world, rows []domain.QueueItem) []domain.QueueItem {
			w.mgr.AddImport(hashA, 203)
			w.mgr.SetHasFile("episode:203", true)
			return rows
		},
		"not settled long enough": nil,
	} {
		t.Run(name, func(t *testing.T) {
			w := packWorld(t, mutate)
			d := 2 * time.Hour
			if mutate == nil {
				d = 20 * time.Minute // under the 30-minute import settle
			}
			w.run(d)
			if r := w.mgr.Removals(); len(r) != 0 {
				t.Fatalf("cleared despite uncertainty: %+v", r)
			}
		})
	}
}

// After a partial pack is cleared, the episode it did not contain gets one
// targeted individual search under the partial_pack permission alone
// (missing_search is not enabled), and then is ordinary missing content.
func TestPartialPackTargetedSearch(t *testing.T) {
	w := packWorld(t, nil)
	action.Tested = append(action.Tested, action.TestedWorkflow{Workflow: policy.WorkflowMissingSearch, Manager: adapter.KindSonarr, ManagerVersion: "0.0.0-fake"})
	w.mgr.SetMissing(fake.MissingEpisode(203, 1, "Show S02E03", t0.Add(-72*time.Hour)), fake.MissingEpisode(150, 1, "Show S01E50", t0.Add(-24*time.Hour)))
	reps := w.runSearches(2 * time.Hour)
	if r := w.mgr.Removals(); len(r) != 1 {
		t.Fatalf("clearance %+v", r)
	}
	if s := w.mgr.Searches(); len(s) == 0 {
		it, _ := w.st.MissingRecord(context.Background(), "sonarr", "episode:203")
		t.Logf("record %+v", it)
		for _, r := range reps[len(reps)-3:] {
			t.Logf("pass %+v", r)
		}
	}
	if s := w.mgr.Searches(); len(s) != 1 || s[0][0] != 203 {
		t.Fatalf("searches %v (want only the pack's missing episode, never a season, never other missing content)", s)
	}
	it, _ := w.st.MissingRecord(context.Background(), "sonarr", "episode:203")
	if it.Origin != "missing" || it.Searches != 1 {
		t.Fatalf("after its targeted search: %+v", it)
	}
	other, _ := w.st.MissingRecord(context.Background(), "sonarr", "episode:150")
	if other == nil || other.Searches != 0 {
		t.Fatalf("ordinary missing content searched without missing_search: %+v", other)
	}
}

func TestQueuedMissingItemsDoNotStarveSearches(t *testing.T) {
	w := searchWorld(t, "automatic")
	var missing []domain.MissingItem
	var queue []domain.QueueItem
	for id := int64(1); id <= 25; id++ {
		missing = append(missing, fake.MissingEpisode(100+id, 1, "Queued episode", t0.Add(-time.Hour)))
		queue = append(queue, fake.Episode(id, hashD, "Queued episode", "Show", 1, id))
	}
	missing = append(missing, fake.MissingEpisode(126, 1, "Unqueued episode", t0.Add(-48*time.Hour)))
	w.mgr.SetMissing(missing...)
	w.mgr.SetQueue(queue...)
	w.runSearches(time.Hour)
	if s := w.mgr.Searches(); len(s) != 1 || s[0][0] != 126 {
		t.Fatalf("searches %v, want the unqueued episode beyond the blocked candidates", s)
	}
}
func TestMissingSearchRechecksFilePresence(t *testing.T) {
	w := searchWorld(t, "automatic")
	w.mgr.SetMissing(fake.MissingEpisode(102, 1, "Already imported", t0.Add(-48*time.Hour)))
	w.mgr.SetHasFile("episode:102", true)
	w.runSearches(time.Hour)
	if s := w.mgr.Searches(); len(s) != 0 {
		t.Fatalf("searched content that already has a file: %v", s)
	}
}

func TestPartialPackRefusesChangedQueueIdentity(t *testing.T) {
	w := packWorld(t, nil)
	ctx := context.Background()
	var fresh map[string]bool
	for i := 0; i < 5; i++ {
		w.clock.Advance(10 * time.Minute)
		fresh = w.eng.Cycle(ctx, nil).Fresh()
	}
	snap, err := w.mgr.Queue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range snap.Items {
		snap.Items[i].QueueID += 100
	}
	w.mgr.SetQueue(snap.Items...)
	if _, err := w.svc.DispatchAutomatic(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	acts, err := w.svc.Actions(ctx, 10)
	if err != nil || len(acts) != 0 {
		t.Fatalf("an intent was reserved for stale queue IDs: %+v, %v", acts, err)
	}
}

func TestPartialPackRefusesMissingPackDiagnostic(t *testing.T) {
	w := packWorld(t, nil)
	ctx := context.Background()
	var fresh map[string]bool
	for i := 0; i < 5; i++ {
		w.clock.Advance(10 * time.Minute)
		fresh = w.eng.Cycle(ctx, nil).Fresh()
	}
	snap, err := w.mgr.Queue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range snap.Items {
		snap.Items[i].StatusMessages = nil
	}
	w.mgr.SetQueue(snap.Items...)
	if _, err := w.svc.DispatchAutomatic(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if r := w.mgr.Removals(); len(r) != 0 {
		t.Fatalf("cleared without the current partial-pack finding from Sonarr: %+v", r)
	}
}

func TestIncompleteMissingListHasActionableFinding(t *testing.T) {
	w := searchWorld(t, "automatic")
	var items []domain.MissingItem
	for id := int64(1); id <= 501; id++ {
		items = append(items, fake.MissingEpisode(id, 1, "Missing episode", t0.Add(-time.Hour)))
	}
	w.mgr.SetMissing(items...)
	w.run(10 * time.Minute)
	ctx := context.Background()
	fs, err := w.st.Findings(ctx, store.FindingFilter{Type: "missing.incomplete"})
	if err != nil || len(fs) != 1 || len(fs[0].Detail.NextSteps) == 0 || fs[0].State != domain.FindingOpen {
		t.Fatalf("truncated missing list needs an actionable finding: %+v, %v", fs, err)
	}
	w.mgr.SetMissing(items[:3]...)
	w.run(time.Hour)
	fs, err = w.st.Findings(ctx, store.FindingFilter{Type: "missing.incomplete"})
	if err != nil || len(fs) != 1 || fs[0].State != domain.FindingResolved {
		t.Fatalf("a complete missing read must resolve the finding: %+v, %v", fs, err)
	}
}
