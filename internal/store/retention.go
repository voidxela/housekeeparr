package store

import "time"

// Retention bounds detailed history. Unresolved findings, their events,
// and transfers referenced by unresolved findings are never pruned.
type Retention struct {
	Observations     time.Duration
	ConnectionChecks time.Duration
	ResolvedFindings time.Duration
	AuditEvents      time.Duration
	// ContentAttemptWindow is the effective content_attempt_window; 0 means
	// attempts count until reset.
	ContentAttemptWindow time.Duration
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
			// Older pack intents recorded their transfer scope only in the finding.
			return run(&out.Findings, `DELETE FROM findings WHERE state='resolved' AND resolved_at < ?
				AND id NOT IN (SELECT i.finding_id FROM action_intents i JOIN missing_content m ON m.origin_intent=i.id
					WHERE m.origin='pack' AND i.finding_id IS NOT NULL
						AND COALESCE(json_extract(i.subject_json, '$.client_id'), '')='')`, ms(now.Add(-r.ResolvedFindings)))
		},
		func() error {
			// A pending pack search still needs its original transfer's policy data.
			return run(&out.Transfers, `DELETE FROM transfers WHERE present=0 AND gone_at < ? AND NOT EXISTS (
				SELECT 1 FROM findings f WHERE f.state != 'resolved'
				AND json_extract(f.subject_json, '$.client_id') = transfers.client_id
				AND json_extract(f.subject_json, '$.native_id') = transfers.native_id)
				AND NOT EXISTS (SELECT 1 FROM missing_content m JOIN action_intents i ON i.id=m.origin_intent
					LEFT JOIN findings f ON f.id=i.finding_id
					WHERE m.origin='pack'
					AND COALESCE(NULLIF(json_extract(i.subject_json, '$.client_id'), ''), json_extract(f.subject_json, '$.client_id')) = transfers.client_id
					AND COALESCE(NULLIF(json_extract(i.subject_json, '$.native_id'), ''), json_extract(f.subject_json, '$.native_id')) = transfers.native_id)`, obsCut)
		},
		func() error {
			return run(&out.QueueItems, "DELETE FROM queue_items WHERE present=0 AND gone_at < ?", obsCut)
		},
		func() error {
			return run(&out.Audit, "DELETE FROM audit_events WHERE at < ?", ms(now.Add(-r.AuditEvents)))
		},
		func() error {
			// Keep the receipts and scope while the scheduler depends on them.
			// Steps and locks cascade when a finished action expires.
			return run(&out.Actions, `DELETE FROM action_intents WHERE state IN ('succeeded','failed','cancelled')
				AND finished_at IS NOT NULL AND finished_at < ?
				AND id NOT IN (SELECT origin_intent FROM missing_content WHERE origin='pack' AND origin_intent IS NOT NULL)
				AND id NOT IN (SELECT last_intent_id FROM missing_content WHERE state='searched' AND last_intent_id IS NOT NULL)`, ms(now.Add(-r.AuditEvents)))
		},
		func() error {
			var n int64
			if err := pruneAttempts(run, &n, now, r.AuditEvents, r.ContentAttemptWindow); err != nil {
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
			if err := run(&n, "DELETE FROM notification_attempts WHERE attempted_at < ?", ms(now.Add(-r.AuditEvents))); err != nil {
				return err
			}
			// Manager history is kept for the audit horizon; coverage then
			// starts no earlier than what is kept.
			if err := run(&n, "DELETE FROM manager_history WHERE at < ?", ms(now.Add(-r.AuditEvents))); err != nil {
				return err
			}
			if err := run(&n, "UPDATE history_cursors SET covered_since=? WHERE covered_since IS NOT NULL AND covered_since < ?",
				ms(now.Add(-r.AuditEvents)), ms(now.Add(-r.AuditEvents))); err != nil {
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

// pruneAttempts removes ledger rows that no longer count and are older
// than the audit horizon, using the same boundaries as budget usage:
//   - pacing reservations, whose windows validation keeps within the
//     horizon;
//   - released attempt rows (a refusal or an unsent action; never counted);
//   - with a rolling attempt window, attempt rows older than both the
//     horizon and the window (they have aged out of the allowance);
//   - without one (reset-only), attempt rows at or before their scope's
//     latest reset. Attempts since the latest reset are durable: they are
//     the outstanding count, kept across pruning and restarts.
//
// Switching from a window back to reset-only therefore counts what is
// still recorded since the latest reset: attempts pruned while the window
// applied (older than the horizon) no longer count.
func pruneAttempts(run func(*int64, string, ...any) error, n *int64, now time.Time, keep, window time.Duration) error {
	cut := ms(now.Add(-keep))
	if err := run(n, "DELETE FROM action_attempts WHERE scope LIKE 'pacing:%' AND at < ?", cut); err != nil {
		return err
	}
	if err := run(n, "DELETE FROM action_attempts WHERE scope LIKE 'attempts:%' AND released_at IS NOT NULL AND at < ?", cut); err != nil {
		return err
	}
	if window > 0 {
		return run(n, "DELETE FROM action_attempts WHERE scope LIKE 'attempts:%' AND at < ?", ms(now.Add(-max(keep, window))))
	}
	return run(n, `DELETE FROM action_attempts WHERE scope LIKE 'attempts:%' AND at < ?
		AND at <= (SELECT max(r.at) FROM action_budget_resets r WHERE r.scope = action_attempts.scope)`, cut)
}
