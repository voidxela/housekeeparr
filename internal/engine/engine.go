// Package engine runs observation cycles: it polls connections with
// bounded concurrency and backoff, persists observations, correlates
// manager queue rows with transfers, evaluates policy, and reconciles
// findings. It performs no operational writes to managers or clients.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/arr"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Clock abstracts time for deterministic tests and demo generation.
type Clock interface {
	Now() time.Time
	// MonoMS is milliseconds of monotonic time since the run started.
	MonoMS() int64
}

// RealClock uses the system clock; Go's time.Since uses the monotonic
// reading, so MonoMS is immune to wall-clock steps.
type RealClock struct{ start time.Time }

// NewRealClock starts a monotonic reference now.
func NewRealClock() *RealClock { return &RealClock{start: time.Now()} }

func (c *RealClock) Now() time.Time { return time.Now() }
func (c *RealClock) MonoMS() int64  { return time.Since(c.start).Milliseconds() }

// Options configure an Engine.
type Options struct {
	Config  *config.Config
	Store   *store.Store
	Conns   *Connections
	Clock   Clock
	Logger  *slog.Logger
	RunKind string // daemon, once, demo
	Version string
}

// Engine owns observation cycles for one run.
type Engine struct {
	cfg     *config.Config
	st      *store.Store
	conns   *Connections
	clock   Clock
	log     *slog.Logger
	runID   string
	kind    string
	version string

	// afterCycle runs after every persisted cycle in Run, outside the
	// cycle's transaction (automatic dispatch).
	afterCycle func(ctx context.Context, rep CycleReport)

	cycleMu   sync.Mutex // serializes snapshot collection and persistence
	queueWall time.Time
	queueMono int64

	mu     sync.Mutex
	probes map[string]probeState
	// missingRead is when each manager's missing list was last read.
	missingRead map[string]time.Time
	last        *CycleReport
	storeErr    error
	started     time.Time
}

type probeState struct {
	probe adapter.Probe
	at    time.Time
}

// ConnResult is the outcome of polling one connection in a cycle.
type ConnResult struct {
	ID      string       `json:"id"`
	Kind    adapter.Kind `json:"kind"`
	Skipped string       `json:"skipped,omitempty"` // reason when not attempted
	OK      bool         `json:"ok"`
	// Complete is false when a successful read returned an incomplete
	// listing.
	Complete   bool          `json:"complete"`
	ErrorClass string        `json:"error_class,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration"`
}

// CycleReport summarises one cycle.
type CycleReport struct {
	StartedAt   time.Time    `json:"started_at"`
	FinishedAt  time.Time    `json:"finished_at"`
	Connections []ConnResult `json:"connections"`
	Opened      int          `json:"opened"`
	Recurred    int          `json:"recurred"`
	Resolved    int          `json:"resolved"`
	StoreError  string       `json:"store_error,omitempty"`
}

// New creates an engine and records the run start.
func New(ctx context.Context, o Options) (*Engine, error) {
	if o.Clock == nil {
		o.Clock = NewRealClock()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	e := &Engine{cfg: o.Config, st: o.Store, conns: o.Conns, clock: o.Clock, log: o.Logger,
		runID: hex.EncodeToString(buf), kind: o.RunKind, version: o.Version, probes: map[string]probeState{}}
	e.started = e.clock.Now()
	if err := e.startRun(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

// RunID identifies this process's run.
func (e *Engine) RunID() string { return e.runID }

// OnCycle installs fn to run after each persisted cycle of Run.
func (e *Engine) OnCycle(fn func(ctx context.Context, rep CycleReport)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.afterCycle = fn
}

// Fresh returns the connections read successfully (and, for listings,
// completely) in rep.
func (rep CycleReport) Fresh() map[string]bool {
	out := map[string]bool{}
	for _, c := range rep.Connections {
		if c.OK && c.Complete {
			out[c.ID] = true
		}
	}
	return out
}

func (e *Engine) startRun(ctx context.Context) error {
	now := e.clock.Now()
	prev, err := e.st.LatestRun(ctx)
	if err != nil {
		return err
	}
	return e.st.Write(ctx, func(tx *store.Tx) error {
		var records []store.ConnectionRecord
		for _, m := range e.conns.Meta {
			status := store.ConnUnknown
			if !m.Enabled {
				status = store.ConnDisabled
			}
			records = append(records, store.ConnectionRecord{ID: m.ID, Kind: string(m.Kind), Name: m.Name, BaseURL: m.BaseURL, Status: status})
		}
		if err := tx.SyncConnections(records, now); err != nil {
			return err
		}
		abandoned, err := tx.StartRun(store.Run{RunID: e.runID, Kind: e.kind, StartedAt: now, PID: os.Getpid(),
			Version: e.version, ConfigRevision: e.cfg.Revision, Mode: string(e.cfg.Mode)})
		if err != nil {
			return err
		}
		if abandoned > 0 {
			if err := tx.Audit(now, "system", "startup.reconcile", fmt.Sprintf("%d previous run(s) ended without a clean shutdown; no outstanding actions exist in this read-only release.", abandoned)); err != nil {
				return err
			}
		}
		if prev != nil && prev.ConfigRevision != e.cfg.Revision {
			if err := tx.Audit(now, "system", "config.changed", fmt.Sprintf("Configuration revision %s → %s. Connection backoff reset.", prev.ConfigRevision, e.cfg.Revision)); err != nil {
				return err
			}
			if err := tx.ResetBackoff(); err != nil {
				return err
			}
		}
		if prev != nil && prev.Mode != string(e.cfg.Mode) {
			if err := tx.Audit(now, "system", "mode.changed", fmt.Sprintf("Mode %s → %s.", prev.Mode, e.cfg.Mode)); err != nil {
				return err
			}
		}
		if prev == nil {
			return tx.Audit(now, "system", "startup.first_run", fmt.Sprintf("First run in mode %q.", e.cfg.Mode))
		}
		return nil
	})
}

// Stop records a clean shutdown.
func (e *Engine) Stop(ctx context.Context, reason string) error {
	return e.st.Write(ctx, func(tx *store.Tx) error { return tx.StopRun(e.runID, e.clock.Now(), reason) })
}

// LastCycle returns the most recent cycle report and any state-store error.
func (e *Engine) LastCycle() (*CycleReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last, e.storeErr
}

// Heartbeat updates the worker heartbeat.
func (e *Engine) Heartbeat(ctx context.Context) error {
	err := e.st.Write(ctx, func(tx *store.Tx) error { return tx.Heartbeat(e.runID, e.clock.Now()) })
	e.setStoreErr(err)
	return err
}

func (e *Engine) setStoreErr(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) {
		e.storeErr = err
	} else if err == nil {
		e.storeErr = nil
	}
}

// Run executes cycles until ctx is cancelled. The first cycle starts after
// a random delay up to the configured jitter, so restarts do not align.
func (e *Engine) Run(ctx context.Context) error {
	delay := jitter(e.cfg.Polling.Jitter.Duration)
	hb := time.NewTicker(30 * time.Second)
	defer hb.Stop()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-hb.C:
			if err := e.Heartbeat(ctx); err != nil {
				e.log.Error("heartbeat failed; state store unavailable", "err", err)
			}
		case <-timer.C:
			next := e.clock.Now().Add(e.cfg.Polling.Interval.Duration + jitter(e.cfg.Polling.Jitter.Duration))
			rep := e.Cycle(ctx, &next)
			if rep.StoreError != "" {
				e.log.Error("cycle could not be persisted; no findings were updated", "err", rep.StoreError)
			} else {
				e.mu.Lock()
				after := e.afterCycle
				e.mu.Unlock()
				if after != nil {
					after(ctx, rep)
				}
			}
			timer.Reset(time.Until(next))
		}
	}
}

func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}

// backoff computes the next attempt after consecutive failures. Ordinary
// transient failures keep the normal cadence at first, then back off
// exponentially; credential and compatibility problems never retry
// rapidly.
func backoff(class adapter.ErrorClass, failures int, interval, retryAfter time.Duration) time.Duration {
	switch class {
	case adapter.ErrUnauthorized:
		return maxDur(time.Hour, interval)
	case adapter.ErrUnsupported, adapter.ErrPermanent:
		return maxDur(30*time.Minute, interval)
	case adapter.ErrRateLimited:
		return maxDur(maxDur(retryAfter, interval), time.Duration(failures)*interval)
	}
	if failures <= 1 {
		return interval
	}
	const ceiling = time.Hour
	d := interval
	for i := 1; i < failures && d < ceiling; i++ {
		d *= 2
	}
	if d > ceiling {
		d = maxDur(ceiling, interval)
	}
	return maxDur(d, retryAfter)
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// fetched holds raw results gathered before persistence.
type fetched struct {
	id       string
	kind     adapter.Kind
	skipped  string
	probe    *adapter.Probe
	transfer *domain.TransferSnapshot
	queue    *domain.QueueSnapshot
	health   *domain.HealthSnapshot
	// commands is the manager's command list, read only while a recovery
	// outcome's replacement search is unsettled; a failed read leaves it
	// nil and fails nothing else.
	commands []arr.Command
	err      error
	op       string
	started  time.Time
	duration time.Duration
}

// LastCycleError reports the most recent state-store error, if any.
func (e *Engine) LastCycleError() error {
	_, err := e.LastCycle()
	return err
}
