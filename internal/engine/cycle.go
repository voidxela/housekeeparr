package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/redact"
	"github.com/voidxela/housekeeparr/internal/store"
)

const probeMaxAge = 6 * time.Hour

// Cycle performs one observation cycle. next, when non-nil, is recorded
// as the next scheduled cycle.
func (e *Engine) Cycle(ctx context.Context, next *time.Time) CycleReport {
	start := e.clock.Now()
	rep := CycleReport{StartedAt: start}
	fail := func(err error) CycleReport {
		e.setStoreErr(err)
		rep.StoreError = err.Error()
		rep.FinishedAt = e.clock.Now()
		e.mu.Lock()
		e.last = &rep
		e.mu.Unlock()
		return rep
	}
	if err := e.st.Write(ctx, func(tx *store.Tx) error { return tx.CycleStarted(e.runID, start) }); err != nil {
		// Never proceed using only in-memory state.
		return fail(fmt.Errorf("state store unwritable; cycle skipped: %w", err))
	}
	records, err := e.st.Connections(ctx)
	if err != nil {
		return fail(err)
	}
	nextAttempt := map[string]*time.Time{}
	for _, r := range records {
		nextAttempt[r.ID] = r.NextAttemptAt
	}

	results := e.fetchAll(ctx, nextAttempt)
	var res findings.Result
	err = e.st.Write(ctx, func(tx *store.Tx) error {
		now := e.clock.Now()
		gone, failures, err := e.persist(tx, results, now)
		if err != nil {
			return err
		}
		r, err := e.evaluate(tx, results, gone, failures, now)
		if err != nil {
			return err
		}
		res = r
		if _, err := findings.ExpireSnoozes(tx, now); err != nil {
			return err
		}
		ret := e.cfg.Retention
		if _, err := tx.Prune(now, store.Retention{Observations: ret.Observations.Duration, ConnectionChecks: ret.ConnectionChecks.Duration,
			ResolvedFindings: ret.ResolvedFindings.Duration, AuditEvents: ret.AuditEvents.Duration}); err != nil {
			return err
		}
		return tx.CycleFinished(e.runID, now, next)
	})
	if err != nil {
		return fail(fmt.Errorf("persist cycle: %w", err))
	}
	e.setStoreErr(nil)
	for _, f := range results {
		cr := ConnResult{ID: f.id, Kind: f.kind, Skipped: f.skipped, OK: f.err == nil && f.skipped == "", Duration: f.duration}
		if f.err != nil {
			cr.ErrorClass, cr.Error = string(adapter.ClassOf(f.err)), errorText(f.err)
		}
		rep.Connections = append(rep.Connections, cr)
	}
	rep.Opened, rep.Recurred, rep.Resolved = len(res.Opened), len(res.Recurred), len(res.Resolved)
	rep.FinishedAt = e.clock.Now()
	for _, f := range append(res.Opened, res.Recurred...) {
		e.log.Info("finding opened", "id", f.ID, "type", f.Type, "severity", f.Severity, "title", f.Title)
	}
	for _, f := range res.Resolved {
		e.log.Info("finding resolved", "id", f.ID, "type", f.Type, "reason", f.Resolution)
	}
	e.mu.Lock()
	e.last = &rep
	e.mu.Unlock()
	return rep
}

func errorText(err error) string {
	var ae *adapter.Error
	if errors.As(err, &ae) {
		if ae.Message != "" {
			return redact.String(ae.Message)
		}
		return ae.Class.Describe()
	}
	return redact.Truncate(redact.String(err.Error()), 200)
}

// fetchAll polls every enabled connection with bounded concurrency.
func (e *Engine) fetchAll(ctx context.Context, nextAttempt map[string]*time.Time) []*fetched {
	sem := make(chan struct{}, e.cfg.Polling.MaxConcurrency)
	var wg sync.WaitGroup
	var out []*fetched
	var mu sync.Mutex
	run := func(id string, kind adapter.Kind, fn func(context.Context, *fetched)) {
		f := &fetched{id: id, kind: kind}
		mu.Lock()
		out = append(out, f)
		mu.Unlock()
		now := e.clock.Now()
		if na := nextAttempt[id]; na != nil && now.Before(*na) {
			f.skipped = fmt.Sprintf("backing off until %s after repeated failures", na.UTC().Format(time.RFC3339))
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			cctx, cancel := context.WithTimeout(ctx, 3*e.cfg.Polling.RequestTimeout.Duration)
			defer cancel()
			f.started = e.clock.Now()
			fn(cctx, f)
			f.duration = e.clock.Now().Sub(f.started)
		}()
	}
	for _, c := range e.conns.Clients {
		c := c
		run(c.ID(), c.Kind(), func(ctx context.Context, f *fetched) {
			if !e.ensureProbe(ctx, c, f) {
				return
			}
			f.op = "list transfers"
			snap, err := c.ListTransfers(ctx)
			if err != nil {
				f.err = err
				e.dropProbe(c.ID())
				return
			}
			snap.CollectedAt = e.clock.Now()
			f.transfer = &snap
		})
	}
	for _, m := range e.conns.Managers {
		m := m
		run(m.ID(), m.Kind(), func(ctx context.Context, f *fetched) {
			if !e.ensureProbe(ctx, m, f) {
				return
			}
			f.op = "read queue"
			q, err := m.Queue(ctx)
			if err != nil {
				f.err = err
				e.dropProbe(m.ID())
				return
			}
			q.CollectedAt = e.clock.Now()
			f.op = "read health"
			h, err := m.Health(ctx)
			if err != nil {
				f.err = err
				e.dropProbe(m.ID())
				return
			}
			h.CollectedAt = e.clock.Now()
			f.queue, f.health = &q, &h
		})
	}
	wg.Wait()
	return out
}

func (e *Engine) ensureProbe(ctx context.Context, c adapter.Connection, f *fetched) bool {
	e.mu.Lock()
	ps, ok := e.probes[c.ID()]
	e.mu.Unlock()
	if ok && e.clock.Now().Sub(ps.at) < probeMaxAge {
		return true
	}
	f.op = "probe"
	p, err := c.Probe(ctx)
	if err != nil {
		f.err = err
		return false
	}
	f.probe = &p
	if p.Compatibility == adapter.CompatUnsupported {
		f.err = &adapter.Error{Class: adapter.ErrUnsupported, Op: string(c.Kind()) + ".probe",
			Message: fmt.Sprintf("%s %s is not supported: %s", p.App, p.Version, p.CompatNote)}
		return false
	}
	e.mu.Lock()
	e.probes[c.ID()] = probeState{probe: p, at: e.clock.Now()}
	e.mu.Unlock()
	return true
}

func (e *Engine) dropProbe(id string) {
	e.mu.Lock()
	delete(e.probes, id)
	e.mu.Unlock()
}

// persist records connection outcomes and snapshots.
func (e *Engine) persist(tx *store.Tx, results []*fetched, now time.Time) (map[string][]store.GoneTransfer, map[string]int, error) {
	gone := map[string][]store.GoneTransfer{}
	failures := map[string]int{}
	interval := e.cfg.Polling.Interval.Duration
	for _, f := range results {
		if f.skipped != "" {
			continue
		}
		if f.probe != nil {
			if err := tx.SetProbe(f.id, *f.probe, now); err != nil {
				return nil, nil, err
			}
		}
		if f.err != nil {
			class := adapter.ClassOf(f.err)
			var retryAfter time.Duration
			var ae *adapter.Error
			if errors.As(f.err, &ae) {
				retryAfter = ae.RetryAfter
			}
			prev, err := tx.Connection(f.id)
			if err != nil {
				return nil, nil, err
			}
			n := 1
			if prev != nil {
				n = prev.ConsecutiveFailures + 1
			}
			msg := fmt.Sprintf("%s failed: %s", f.op, errorText(f.err))
			count, err := tx.RecordFailure(f.id, now, string(class), msg, now.Add(backoff(class, n, interval, retryAfter)))
			if err != nil {
				return nil, nil, err
			}
			failures[f.id] = count
			if err := tx.RecordCheck(f.id, store.ConnectionCheck{At: now, Op: f.op, OK: false, ErrorClass: string(class), Message: msg, DurationMS: f.duration.Milliseconds()}); err != nil {
				return nil, nil, err
			}
			e.log.Warn("connection read failed", "connection", f.id, "op", f.op, "class", class, "error", errorText(f.err), "consecutive_failures", count)
			continue
		}
		if err := tx.RecordSuccess(f.id, now); err != nil {
			return nil, nil, err
		}
		if err := tx.RecordCheck(f.id, store.ConnectionCheck{At: now, Op: "read", OK: true, DurationMS: f.duration.Milliseconds()}); err != nil {
			return nil, nil, err
		}
		if f.transfer != nil {
			g, err := tx.SaveTransferSnapshot(*f.transfer, e.runID, e.clock.MonoMS(), e.cfg.Retention.MaxObservationsPerTransfer)
			if err != nil {
				return nil, nil, err
			}
			gone[f.id] = g
		}
		if f.queue != nil {
			if _, err := tx.SaveQueueSnapshot(*f.queue); err != nil {
				return nil, nil, err
			}
		}
		if f.health != nil {
			if err := tx.SetHealth(f.id, *f.health); err != nil {
				return nil, nil, err
			}
		}
	}
	return gone, failures, nil
}

// evaluate correlates and reconciles findings for every scope with fresh
// evidence. Scopes without fresh evidence are left untouched.
func (e *Engine) evaluate(tx *store.Tx, results []*fetched, gone map[string][]store.GoneTransfer, failures map[string]int, now time.Time) (findings.Result, error) {
	var total findings.Result
	add := func(r findings.Result) {
		total.Opened = append(total.Opened, r.Opened...)
		total.Recurred = append(total.Recurred, r.Recurred...)
		total.Updated = append(total.Updated, r.Updated...)
		total.Resolved = append(total.Resolved, r.Resolved...)
	}
	fresh := map[string]bool{} // clients with a complete listing this cycle
	for _, f := range results {
		if f.transfer != nil && f.err == nil && f.transfer.Complete {
			fresh[f.id] = true
		}
	}
	// 1. Correlate queue rows with transfers.
	for _, f := range results {
		if f.queue == nil || f.err != nil {
			continue
		}
		if err := e.correlate(tx, f.id, fresh, now); err != nil {
			return total, err
		}
	}
	// 2. Transfer diagnoses.
	for _, f := range results {
		if f.transfer == nil || f.err != nil {
			continue
		}
		r, err := e.evaluateClient(tx, *f.transfer, gone[f.id], now)
		if err != nil {
			return total, err
		}
		add(r)
	}
	// 3. Queue and health findings.
	for _, f := range results {
		if f.queue == nil || f.err != nil {
			continue
		}
		r, err := e.evaluateQueue(tx, f.id, *f.queue, now)
		if err != nil {
			return total, err
		}
		add(r)
		r, err = e.evaluateHealth(tx, f.id, *f.health, now)
		if err != nil {
			return total, err
		}
		add(r)
	}
	// 4. Connection health.
	for _, f := range results {
		if f.skipped != "" {
			continue
		}
		r, err := e.evaluateConnection(tx, f, failures[f.id], now)
		if err != nil {
			return total, err
		}
		add(r)
	}
	return total, nil
}

func (e *Engine) correlate(tx *store.Tx, managerID string, fresh map[string]bool, now time.Time) error {
	meta := e.conns.Meta[managerID]
	items, err := tx.QueueItems(store.QueueFilter{ManagerID: managerID, PresentOnly: true})
	if err != nil {
		return err
	}
	for _, it := range items {
		q := it.Latest
		status, clientID, nativeID, note := store.MatchUnknown, "", "", ""
		switch {
		case q.Protocol != "" && q.Protocol != "torrent":
			status, note = store.MatchNotTorrent, fmt.Sprintf("%s download; Housekeeparr v1 observes torrent workflows only", q.Protocol)
		case q.DownloadID == "":
			status, note = store.MatchNoDownloadID, "no download yet (pending or delayed release)"
		default:
			refs, err := tx.TransfersByHash(q.DownloadID)
			if err != nil {
				return err
			}
			var present []store.TransferRef
			for _, r := range refs {
				if r.Present {
					present = append(present, r)
				}
			}
			mapped, hasMap := meta.DownloadClients[q.DownloadClient]
			switch {
			case hasMap:
				var inMapped, other []store.TransferRef
				for _, r := range present {
					if r.ClientID == mapped {
						inMapped = append(inMapped, r)
					} else {
						other = append(other, r)
					}
				}
				switch {
				case len(inMapped) == 1:
					status, clientID, nativeID = store.MatchMatched, mapped, inMapped[0].NativeID
					note = fmt.Sprintf("infohash matched in %s, which Sonarr's download client %q maps to", mapped, q.DownloadClient)
					if len(other) > 0 {
						note += fmt.Sprintf("; the same infohash is also present in %d other client(s), so data may be shared", len(other))
					}
				case len(inMapped) > 1:
					status, note = store.MatchAmbiguous, fmt.Sprintf("%d transfers in %s match this download id", len(inMapped), mapped)
				case fresh[mapped]:
					status, clientID, note = store.MatchNotFound, mapped, fmt.Sprintf("not present in a complete listing of %s", mapped)
				default:
					status, clientID, note = store.MatchClientUnavailable, mapped, fmt.Sprintf("%s has no fresh complete listing this cycle; correlation unknown", mapped)
				}
			case len(present) == 1:
				status, clientID, nativeID = store.MatchHashOnly, present[0].ClientID, present[0].NativeID
				note = fmt.Sprintf("matched by infohash only; Sonarr download client %q is not mapped in download_clients", q.DownloadClient)
			case len(present) > 1:
				status, note = store.MatchAmbiguous, fmt.Sprintf("infohash present in %d clients and Sonarr download client %q is not mapped", len(present), q.DownloadClient)
			default:
				status, note = store.MatchNotConfigured, fmt.Sprintf("Sonarr download client %q is not mapped to a Housekeeparr connection and no configured client holds this infohash", q.DownloadClient)
			}
		}
		if _, err := tx.SetQueueMatch(managerID, it.ItemKey, status, clientID, nativeID, note, now); err != nil {
			return err
		}
	}
	return nil
}
