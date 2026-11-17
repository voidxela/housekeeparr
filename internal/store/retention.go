package store

import "time"

// Retention bounds detailed history. Unresolved findings, their events,
// and transfers referenced by unresolved findings are never pruned.
type Retention struct {
	Observations     time.Duration
	ConnectionChecks time.Duration
	ResolvedFindings time.Duration
	AuditEvents      time.Duration
}

// PruneResult counts removed rows.
type PruneResult struct {
	Observations, Checks, Findings, Transfers, QueueItems, Audit, Runs, Actions int64
}

// Prune removes expired history.
func (t *Tx) Prune(now time.Time, r Retention) (PruneResult, error) {
	var out PruneResult
	run := func(dst *int64, q string, args ...any) error {
		res, err := t.exec(q, args...)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		*dst += n
		return nil
	}
	obsCut := ms(now.Add(-r.Observations))
	steps := []func() error{
		func() error {
			return run(&out.Observations, "DELETE FROM transfer_observations WHERE at < ?", obsCut)
		},
		func() error {
			return run(&out.Checks, "DELETE FROM connection_checks WHERE at < ?", ms(now.Add(-r.ConnectionChecks)))
		},
		func() error {
			return run(&out.Findings, "DELETE FROM findings WHERE state='resolved' AND resolved_at < ?", ms(now.Add(-r.ResolvedFindings)))
		},
		func() error {
			// Gone transfers are kept while any unresolved finding refers to them.
			return run(&out.Transfers, `DELETE FROM transfers WHERE present=0 AND gone_at < ? AND NOT EXISTS (
				SELECT 1 FROM findings f WHERE f.state != 'resolved'
				AND json_extract(f.subject_json, '$.client_id') = transfers.client_id
				AND json_extract(f.subject_json, '$.native_id') = transfers.native_id)`, obsCut)
		},
		func() error {
			return run(&out.QueueItems, "DELETE FROM queue_items WHERE present=0 AND gone_at < ?", obsCut)
		},
		func() error {
			return run(&out.Audit, "DELETE FROM audit_events WHERE at < ?", ms(now.Add(-r.AuditEvents)))
		},
		func() error {
			// Only finished actions expire; uncertain and active ones (and
			// their locks) are never pruned. Steps and locks cascade.
			return run(&out.Actions, `DELETE FROM action_intents WHERE state IN ('succeeded','failed','cancelled')
				AND finished_at IS NOT NULL AND finished_at < ?`, ms(now.Add(-r.AuditEvents)))
		},
		func() error {
			var n int64
			if err := pruneAttempts(run, &n, now, r.AuditEvents); err != nil {
				return err
			}
			return run(&n, "DELETE FROM action_cooldowns WHERE until < ?", ms(now))
		},
		func() error {
			var n int64
			if err := run(&n, "DELETE FROM automation_evaluations WHERE finding_id NOT IN (SELECT id FROM findings)"); err != nil {
				return err
			}
			var gone int64
			if err := run(&gone, "DELETE FROM missing_content WHERE present=0 AND gone_at < ?", ms(now.Add(-r.AuditEvents))); err != nil {
				return err
			}
			var notes int64
			if err := run(&notes, "DELETE FROM notification_events WHERE (sent_at IS NOT NULL OR dropped_at IS NOT NULL) AND created_at < ?", ms(now.Add(-r.AuditEvents))); err != nil {
				return err
			}
			return run(&out.Runs, "DELETE FROM runs WHERE stopped_at IS NOT NULL AND stopped_at < ?", ms(now.Add(-r.AuditEvents)))
		},
	}
	for _, s := range steps {
		if err := s(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// pruneAttempts removes pacing reservations older than the retention
// period (which validation keeps longer than any pacing window) and
// attempt rows that predate their scope's latest reset. Attempts since the
// latest reset are durable: they are the per-content retry history.
func pruneAttempts(run func(*int64, string, ...any) error, n *int64, now time.Time, keep time.Duration) error {
	if err := run(n, "DELETE FROM action_attempts WHERE scope LIKE 'pacing:%' AND at < ?", ms(now.Add(-keep))); err != nil {
		return err
	}
	return run(n, `DELETE FROM action_attempts WHERE scope LIKE 'attempts:%' AND at < ?
		AND at < (SELECT max(r.at) FROM action_budget_resets r WHERE r.scope = action_attempts.scope)`, ms(now.Add(-keep)))
}
