// Package app holds the application services shared by the CLI and the web
// interface. Neither interface implements policy or reads adapters itself:
// both call these services, which read local state only (so viewing never
// triggers external work) and route the few local mutations through the
// findings lifecycle.
package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// EngineStatus is the optional in-process view of the worker.
type EngineStatus interface {
	LastCycleError() error
}

// Service is the shared application service.
type Service struct {
	st     *store.Store
	cfg    *config.Config
	now    func() time.Time
	engine EngineStatus // nil when not running in the worker process
	// executor performs action steps; without one, actions are refused
	// with their prerequisites.
	executor action.Executor
	// execMu serialises every execution path (manual actions and
	// automatic dispatch, each with its reconciliation), so one cannot
	// reconcile the other's in-flight intent, and only one acquisition
	// is in flight at a time (SPEC §9.1).
	execMu sync.Mutex
}

// SetExecutor installs the action executor (tests and future workflows).
func (s *Service) SetExecutor(e action.Executor) { s.executor = e }

// New creates a service. engine may be nil.
func New(st *store.Store, cfg *config.Config, now func() time.Time, engine EngineStatus) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, cfg: cfg, now: now, engine: engine}
}

// Origin reports whether data is live or synthetic demo data.
func (s *Service) Origin() store.Origin { return s.st.Origin() }

// Config returns the active configuration.
func (s *Service) Config() *config.Config { return s.cfg }

// Now returns the service clock reading.
func (s *Service) Now() time.Time { return s.now() }

// Freshness classifies the age of evidence.
type Freshness string

const (
	FreshNever Freshness = "never"
	FreshOK    Freshness = "fresh"
	FreshStale Freshness = "stale"
)

func (s *Service) staleAfter() time.Duration {
	return 2*s.cfg.Polling.Interval.Duration + s.cfg.Polling.Jitter.Duration + time.Minute
}

func (s *Service) freshness(at *time.Time) Freshness {
	switch {
	case at == nil:
		return FreshNever
	case s.now().Sub(*at) > s.staleAfter():
		return FreshStale
	default:
		return FreshOK
	}
}

// WorkerState summarises the worker heartbeat.
type WorkerState string

const (
	WorkerRunning WorkerState = "running"
	WorkerStale   WorkerState = "stale"   // no heartbeat recently; process may be hung or killed
	WorkerStopped WorkerState = "stopped" // stopped cleanly
	WorkerNever   WorkerState = "never"   // never ran
	// WorkerScheduled means one-shot runs (from an external scheduler) are
	// completing recently enough for evidence to stay fresh.
	WorkerScheduled WorkerState = "scheduled"
)

// Healthy reports whether evidence is being refreshed.
func (w Worker) Healthy() bool { return w.State == WorkerRunning || w.State == WorkerScheduled }

// Worker describes the worker process.
type Worker struct {
	State        WorkerState `json:"state"`
	Run          *store.Run  `json:"run,omitempty"`
	HeartbeatAge string      `json:"heartbeat_age,omitempty"`
	Explanation  string      `json:"explanation"`
}

// ConnectionView is a connection with derived freshness.
type ConnectionView struct {
	store.ConnectionRecord
	Freshness   Freshness `json:"freshness"`
	Age         string    `json:"age,omitempty"`
	Explanation string    `json:"explanation"`
}

// Overview is the landing summary.
type Overview struct {
	GeneratedAt    time.Time             `json:"generated_at"`
	Origin         store.Origin          `json:"data_origin"`
	Mode           config.Mode           `json:"mode"`
	ConfigRevision string                `json:"config_revision"`
	Worker         Worker                `json:"worker"`
	Counts         store.FindingCounts   `json:"finding_counts"`
	Connections    []ConnectionView      `json:"connections"`
	Recent         []domain.FindingEvent `json:"recent_events"`
	Transfers      TransferCounts        `json:"transfers"`
	Warnings       []string              `json:"warnings,omitempty"`
	Capabilities   []string              `json:"milestone_limitations"`
	// Automation counts findings by their latest automatic evaluation.
	Automation map[string]int `json:"automation,omitempty"`
}

// TransferCounts summarises observed transfers.
type TransferCounts struct {
	Incomplete int `json:"incomplete"`
	Observing  int `json:"observing"`
	Diagnosed  int `json:"diagnosed"`
}

// MilestoneLimitations are shown wherever operators look for actions.
var MilestoneLimitations = []string{
	"Observe mode (the default) never changes media managers or download clients. Outside it, writes are limited to workflows with a recorded " +
		"real-instance test for the exact versions: today no-progress and metadata recovery for Sonarr 4.0.20 or Radarr 6.4.4 with qBittorrent 5.3.0, Transmission 4.0.6 or 4.1.4, Deluge 2.2.0, or rTorrent 0.9.8, and only when the manager redownloads failed grabs.",
	"Preserved-data failure handling acts only on a tracker's confirmed \"not registered\" rejection, and only where it has been tested; partial-pack handling, missing-content searches, and notifications are planned for v1 but not implemented yet.",
	"Observation is validated only for the released versions listed in docs/compatibility.md; other versions are untested.",
}

// Overview builds the landing summary.
func (s *Service) Overview(ctx context.Context) (*Overview, error) {
	now := s.now()
	o := &Overview{GeneratedAt: now, Origin: s.st.Origin(), Mode: s.cfg.Mode, ConfigRevision: s.cfg.Revision,
		Capabilities: MilestoneLimitations, Warnings: append([]string(nil), s.st.Warnings()...)}
	w, err := s.Worker(ctx)
	if err != nil {
		return nil, err
	}
	o.Worker = *w
	if o.Counts, err = s.st.CountFindings(ctx); err != nil {
		return nil, err
	}
	if o.Connections, err = s.Connections(ctx); err != nil {
		return nil, err
	}
	if o.Recent, err = s.st.RecentFindingEvents(ctx, 8); err != nil {
		return nil, err
	}
	if o.Automation, err = s.st.EvaluationCounts(ctx); err != nil {
		return nil, err
	}
	trs, err := s.st.Transfers(ctx, store.TransferFilter{PresentOnly: true})
	if err != nil {
		return nil, err
	}
	for _, t := range trs {
		o.Transfers.Incomplete++
		if t.Assessment != nil {
			switch t.Assessment.Status {
			case policy.StatusObserving, policy.StatusGrace:
				o.Transfers.Observing++
			case policy.StatusQualified:
				o.Transfers.Diagnosed++
			}
		}
	}
	if s.engine != nil {
		if err := s.engine.LastCycleError(); err != nil {
			o.Warnings = append(o.Warnings, "State store error: "+err.Error()+". No observations or findings are being updated until it is fixed.")
		}
	}
	return o, nil
}

// Worker reports the heartbeat-derived worker state.
func (s *Service) Worker(ctx context.Context) (*Worker, error) {
	run, err := s.st.LatestRun(ctx)
	if err != nil {
		return nil, err
	}
	w := &Worker{Run: run}
	switch {
	case run == nil:
		w.State, w.Explanation = WorkerNever, "Housekeeparr has not run yet."
	case run.Kind == "once" && run.LastCycleFinishedAt != nil:
		age := s.now().Sub(*run.LastCycleFinishedAt)
		w.HeartbeatAge = policy.HumanDuration(age) + " ago"
		if age > s.staleAfter() {
			w.State = WorkerStale
			w.Explanation = fmt.Sprintf("The last one-shot run finished %s ago; is the external scheduler still running?", policy.HumanDuration(age))
		} else {
			w.State = WorkerScheduled
			w.Explanation = fmt.Sprintf("One-shot runs are current (last finished %s ago).", policy.HumanDuration(age))
		}
	case run.StoppedAt != nil:
		w.State = WorkerStopped
		w.Explanation = fmt.Sprintf("Stopped %s ago (%s). Evidence is not being refreshed.", policy.HumanDuration(s.now().Sub(*run.StoppedAt)), run.StopReason)
	default:
		age := s.now().Sub(run.HeartbeatAt)
		w.HeartbeatAge = policy.HumanDuration(age) + " ago"
		if age < 30*time.Second {
			w.HeartbeatAge = "just now"
		}
		if age > 2*time.Minute+s.cfg.Polling.RequestTimeout.Duration*3 {
			w.State = WorkerStale
			w.Explanation = fmt.Sprintf("No worker heartbeat for %s; the process may have stopped or hung.", w.HeartbeatAge)
		} else {
			w.State = WorkerRunning
			w.Explanation = "Worker heartbeat is current."
			if run.NextCycleAt != nil {
				d := run.NextCycleAt.Sub(s.now())
				if d < 0 {
					d = 0
				}
				w.Explanation += fmt.Sprintf(" Next poll in %s.", policy.HumanDuration(d))
			}
		}
	}
	return w, nil
}

// Connections lists configured connections with freshness.
func (s *Service) Connections(ctx context.Context) ([]ConnectionView, error) {
	recs, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	var out []ConnectionView
	for _, r := range recs {
		if !r.Configured {
			continue
		}
		v := ConnectionView{ConnectionRecord: r, Freshness: s.freshness(r.LastSuccessAt)}
		if r.LastSuccessAt != nil {
			v.Age = policy.HumanDuration(s.now().Sub(*r.LastSuccessAt))
		}
		switch {
		case r.Status == store.ConnDisabled:
			v.Explanation = "Disabled in configuration."
		case r.LastSuccessAt == nil && r.LastAttemptAt == nil:
			v.Explanation = "Not polled yet."
		case r.Status == store.ConnFailing:
			v.Explanation = fmt.Sprintf("Failing (%d consecutive): %s", r.ConsecutiveFailures, r.LastError)
		case v.Freshness == FreshStale:
			v.Explanation = fmt.Sprintf("Last successful read %s ago; evidence is stale.", v.Age)
		default:
			v.Explanation = fmt.Sprintf("Last successful read %s ago.", v.Age)
		}
		out = append(out, v)
	}
	return out, nil
}

// FindingQuery selects findings.
type FindingQuery struct {
	States []domain.FindingState
	Limit  int
}

// ActiveStates are the unresolved finding states.
var ActiveStates = []domain.FindingState{domain.FindingOpen, domain.FindingAcknowledged, domain.FindingSnoozed}

// FindingView adds derived freshness to a finding.
type FindingView struct {
	domain.Finding
	EvidenceAge string    `json:"evidence_age"`
	Freshness   Freshness `json:"evidence_freshness"`
}

func (s *Service) view(f domain.Finding) FindingView {
	at := f.EvidenceAt
	v := FindingView{Finding: f, EvidenceAge: policy.HumanDuration(s.now().Sub(at)), Freshness: s.freshness(&at)}
	if f.State == domain.FindingResolved {
		v.Freshness = FreshOK
	}
	return v
}

// Findings lists findings, unresolved by default.
func (s *Service) Findings(ctx context.Context, q FindingQuery) ([]FindingView, error) {
	if len(q.States) == 0 {
		q.States = ActiveStates
	}
	if q.Limit == 0 {
		q.Limit = 500
	}
	fs, err := s.st.Findings(ctx, store.FindingFilter{States: q.States, Limit: q.Limit})
	if err != nil {
		return nil, err
	}
	out := make([]FindingView, 0, len(fs))
	for _, f := range fs {
		out = append(out, s.view(f))
	}
	return out, nil
}

// FindingDetail is a finding with its lifecycle and earlier occurrences.
type FindingDetail struct {
	FindingView
	Events  []domain.FindingEvent `json:"events"`
	History []domain.Finding      `json:"previous_occurrences,omitempty"`
	// Automation is the latest automatic evaluation, if any.
	Automation *store.AutomationEvaluation `json:"automation,omitempty"`
}

// ErrNotFound is returned for unknown IDs.
var ErrNotFound = errors.New("not found")

// Finding returns one finding with its history.
func (s *Service) Finding(ctx context.Context, id int64) (*FindingDetail, error) {
	f, err := s.st.Finding(ctx, id)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, ErrNotFound
	}
	d := &FindingDetail{FindingView: s.view(*f)}
	if d.Events, err = s.st.FindingEvents(ctx, id); err != nil {
		return nil, err
	}
	if d.History, err = s.st.FindingHistory(ctx, *f, 10); err != nil {
		return nil, err
	}
	if d.Automation, err = s.st.Evaluation(ctx, id); err != nil {
		return nil, err
	}
	return d, nil
}

// DownloadView joins a transfer with its manager relationships.
type DownloadView struct {
	Transfer  store.TransferRecord `json:"transfer"`
	Queue     []store.QueueRecord  `json:"queue"`
	Findings  []FindingView        `json:"findings"`
	Freshness Freshness            `json:"freshness"`
	Age       string               `json:"age"`
	Progress  *float64             `json:"progress,omitempty"`
}

// Downloads is the downloads view.
type Downloads struct {
	Transfers []DownloadView      `json:"transfers"`
	Unmatched []store.QueueRecord `json:"queue_without_transfer"`
}

// Downloads lists present incomplete transfers and queue rows without a
// correlated transfer. includeComplete adds complete transfers.
func (s *Service) Downloads(ctx context.Context, includeComplete bool) (*Downloads, error) {
	trs, err := s.st.Transfers(ctx, store.TransferFilter{PresentOnly: true, IncludeDone: includeComplete, Limit: 2000})
	if err != nil {
		return nil, err
	}
	queue, err := s.st.QueueItems(ctx, store.QueueFilter{PresentOnly: true})
	if err != nil {
		return nil, err
	}
	conns, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	lastOK := map[string]*time.Time{}
	for _, c := range conns {
		lastOK[c.ID] = c.LastSuccessAt
	}
	active, err := s.Findings(ctx, FindingQuery{})
	if err != nil {
		return nil, err
	}
	byTransfer := map[string][]FindingView{}
	for _, f := range active {
		if f.Subject.ClientID != "" && f.Subject.NativeID != "" {
			k := f.Subject.ClientID + "|" + f.Subject.NativeID
			byTransfer[k] = append(byTransfer[k], f)
		}
	}
	byMatch := map[string][]store.QueueRecord{}
	out := &Downloads{}
	for _, q := range queue {
		if q.MatchClientID != "" && q.MatchNativeID != "" {
			byMatch[q.MatchClientID+"|"+q.MatchNativeID] = append(byMatch[q.MatchClientID+"|"+q.MatchNativeID], q)
		} else {
			out.Unmatched = append(out.Unmatched, q)
		}
	}
	for _, t := range trs {
		k := t.ClientID + "|" + t.NativeID
		v := DownloadView{Transfer: t, Queue: byMatch[k], Findings: byTransfer[k], Freshness: s.freshness(lastOK[t.ClientID])}
		v.Transfer.LastSeen = seenAt(t, lastOK[t.ClientID])
		v.Age = policy.HumanDuration(s.now().Sub(v.Transfer.LastSeen))
		if p, ok := t.Latest.Progress(); ok {
			v.Progress = &p
		}
		out.Transfers = append(out.Transfers, v)
	}
	sort.SliceStable(out.Transfers, func(i, j int) bool {
		return downloadRank(out.Transfers[i]) < downloadRank(out.Transfers[j])
	})
	return out, nil
}

func downloadRank(d DownloadView) int {
	switch {
	case len(d.Findings) > 0:
		return 0
	case d.Transfer.Assessment != nil && d.Transfer.Assessment.Status == policy.StatusObserving:
		return 1
	case d.Transfer.StateClass == domain.StateComplete:
		return 3
	default:
		return 2
	}
}

// DownloadDetail adds observation history.
type DownloadDetail struct {
	DownloadView
	Observations []store.Observation `json:"observations"`
	Policy       policy.Effective    `json:"effective_policy"`
}

// Download returns one transfer with history.
func (s *Service) Download(ctx context.Context, clientID, nativeID string) (*DownloadDetail, error) {
	t, err := s.st.Transfer(ctx, clientID, nativeID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, ErrNotFound
	}
	queue, err := s.st.QueueItems(ctx, store.QueueFilter{ClientID: clientID, NativeID: nativeID})
	if err != nil {
		return nil, err
	}
	fs, err := s.st.Findings(ctx, store.FindingFilter{ClientID: clientID, NativeID: nativeID, Limit: 50})
	if err != nil {
		return nil, err
	}
	conns, err := s.st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	var lastOK *time.Time
	for _, c := range conns {
		if c.ID == clientID {
			lastOK = c.LastSuccessAt
		}
	}
	t.LastSeen = seenAt(*t, lastOK)
	d := &DownloadDetail{DownloadView: DownloadView{Transfer: *t, Queue: queue, Freshness: s.freshness(lastOK),
		Age: policy.HumanDuration(s.now().Sub(t.LastSeen))}}
	for _, f := range fs {
		d.Findings = append(d.Findings, s.view(f))
	}
	if p, ok := t.Latest.Progress(); ok {
		d.Progress = &p
	}
	if d.Observations, err = s.st.Observations(ctx, clientID, nativeID, s.now().Add(-48*time.Hour), 300); err != nil {
		return nil, err
	}
	// The transfer's own effective policy (rules and overrides included),
	// exactly what its diagnosis and any action use.
	latest := t.Latest
	if eff, _, err := s.cfg.ResolveTransfer(policy.TransferSubject(clientID, nativeID, &latest, presentAssociations(queue))); err == nil {
		d.Policy = eff
	} else {
		d.Policy, _ = s.cfg.EffectivePolicy("")
	}
	return d, nil
}

// Policies returns the effective policy for every scope.
func (s *Service) Policies() ([]policy.Effective, error) {
	g, err := s.cfg.EffectivePolicy("")
	if err != nil {
		return nil, err
	}
	out := []policy.Effective{g}
	for _, ce := range s.cfg.Clients() {
		e, err := s.cfg.EffectivePolicy(ce.Config.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// Activity is the durable decision/audit history.
type Activity struct {
	Events  []domain.FindingEvent `json:"finding_events"`
	Audit   []store.AuditEvent    `json:"audit_events"`
	Runs    []store.Run           `json:"runs"`
	Actions []store.Intent        `json:"actions"`
	// Outcomes are what was observed after each verified recovery, by
	// action ID.
	Outcomes map[int64]store.RecoveryOutcome `json:"outcomes,omitempty"`
	// Retained lists transfers recovery stopped and kept with their data,
	// against the configured ceiling.
	Retained      []store.RetainedTransfer `json:"retained,omitempty"`
	RetainedUsage store.RetainedUsage      `json:"retained_usage"`
	Limits        policy.Limits            `json:"limits"`
	Journal       JournalStatus            `json:"journal"`
	// Attempts are per-content recovery attempts since their latest reset.
	Attempts []AttemptSummary `json:"attempts,omitempty"`
	// Notifications are the latest notification events and their delivery.
	Notifications     []store.Notification `json:"notifications,omitempty"`
	NotifyTargets     int                  `json:"notify_targets"`
	NotifyMaxAttempts int                  `json:"notify_max_attempts"`
	NotifyMaxAge      string               `json:"notify_max_age"`
	// Missing is recorded missing content with Housekeeparr's search state.
	Missing       []store.MissingRecord `json:"missing,omitempty"`
	SearchEnabled bool                  `json:"search_enabled"`
	Note          string                `json:"note"`
}

// Outcomes returns the replacement outcomes of the given actions.
func (s *Service) Outcomes(ctx context.Context, actions []store.Intent) (map[int64]store.RecoveryOutcome, error) {
	ids := make([]int64, 0, len(actions))
	for _, in := range actions {
		ids = append(ids, in.ID)
	}
	return s.st.Outcomes(ctx, ids)
}

// Activity returns recent lifecycle and audit history.
func (s *Service) Activity(ctx context.Context, limit int) (*Activity, error) {
	a := &Activity{Note: "Every action is journaled before any request is sent, and each step is verified by observation. Only tested workflows can run, manually outside observe mode or automatically in automatic mode when enabled; uncertain outcomes are held for review, never retried. After a verified recovery, the manager's replacement search and the replacement grab, its progress, and completion are observed separately."}
	var err error
	if a.Actions, err = s.st.Intents(ctx, store.IntentFilter{Limit: limit}); err != nil {
		return nil, err
	}
	if a.Outcomes, err = s.Outcomes(ctx, a.Actions); err != nil {
		return nil, err
	}
	if a.Retained, a.RetainedUsage, err = s.Retained(ctx, false); err != nil {
		return nil, err
	}
	a.Limits = s.cfg.Limits()
	if a.Attempts, err = s.AttemptSummaries(ctx); err != nil {
		return nil, err
	}
	if a.Notifications, err = s.st.RecentNotifications(ctx, 20); err != nil {
		return nil, err
	}
	a.NotifyTargets = len(s.cfg.Notify)
	a.NotifyMaxAttempts = s.cfg.Notifications.MaxAttempts
	a.NotifyMaxAge = policy.HumanDuration(s.cfg.Notifications.MaxAge.Duration)
	if a.Missing, err = s.st.MissingContent(ctx, store.MissingFilter{PresentOnly: true, Limit: 25}); err != nil {
		return nil, err
	}
	a.SearchEnabled = s.cfg.MaybeEnabled(policy.WorkflowMissingSearch)
	if a.Journal, err = s.Journal(ctx); err != nil {
		return nil, err
	}
	if a.Events, err = s.st.RecentFindingEvents(ctx, limit); err != nil {
		return nil, err
	}
	if a.Audit, err = s.st.RecentAudit(ctx, limit); err != nil {
		return nil, err
	}
	if a.Runs, err = s.st.RecentRuns(ctx, 10); err != nil {
		return nil, err
	}
	return a, nil
}

// Actor identifies who performed a local mutation.
type Actor string

const (
	ActorCLI Actor = "cli"
	ActorWeb Actor = "web"
)

func (s *Service) mutate(ctx context.Context, actor Actor, fn func(*store.Tx, time.Time) (*domain.Finding, error)) (*domain.Finding, error) {
	var out *domain.Finding
	err := s.st.Write(ctx, func(tx *store.Tx) error {
		f, err := fn(tx, s.now())
		out = f
		return err
	})
	if errors.Is(err, findings.ErrNotFound) {
		return nil, ErrNotFound
	}
	return out, err
}

// Acknowledge marks a finding as seen.
func (s *Service) Acknowledge(ctx context.Context, id int64, actor Actor) (*domain.Finding, error) {
	return s.mutate(ctx, actor, func(tx *store.Tx, now time.Time) (*domain.Finding, error) {
		return findings.Acknowledge(tx, id, string(actor), now)
	})
}

// Snooze suppresses notifications/automation for a finding for d.
func (s *Service) Snooze(ctx context.Context, id int64, d time.Duration, actor Actor) (*domain.Finding, error) {
	return s.mutate(ctx, actor, func(tx *store.Tx, now time.Time) (*domain.Finding, error) {
		return findings.Snooze(tx, id, d, string(actor), now)
	})
}

// Reopen returns a finding to open.
func (s *Service) Reopen(ctx context.Context, id int64, actor Actor) (*domain.Finding, error) {
	return s.mutate(ctx, actor, func(tx *store.Tx, now time.Time) (*domain.Finding, error) {
		return findings.Reopen(tx, id, string(actor), now)
	})
}

// seenAt is when a present transfer was last confirmed. Unchanged complete
// transfers are not rewritten each cycle, so their presence is confirmed by
// the client's last successful (complete) listing instead.
func seenAt(t store.TransferRecord, lastOK *time.Time) time.Time {
	if t.Present && t.StateClass == domain.StateComplete && lastOK != nil && lastOK.After(t.LastSeen) {
		return *lastOK
	}
	return t.LastSeen
}
