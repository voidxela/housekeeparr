package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Recovery outcome states.
const (
	OutcomeAwaitingGrab = "awaiting_grab" // no replacement grab observed yet
	OutcomeNoGrabYet    = "no_grab_yet"   // none observed for a while; still watching
	OutcomeGrabbed      = "grabbed"       // a replacement release was grabbed
	OutcomeProgressing  = "progressing"   // its verified bytes increased
	OutcomeCompleted    = "completed"     // its download completed
	OutcomeUntracked    = "untracked"     // tracking window ended without completion
)

// Replacement search states (schema 10). The search is observed in the
// manager's command list, separately from the grab.
const (
	SearchNotTracked    = "not_tracked"    // recorded before search tracking existed
	SearchUnobserved    = "unobserved"     // not seen yet; still looking
	SearchAccepted      = "accepted"       // queued by the manager
	SearchRunning       = "running"        // started
	SearchCompleted     = "completed"      // finished; says nothing about a grab
	SearchFailed        = "failed"         // failed, aborted, cancelled, or orphaned
	SearchNotObserved   = "not_observed"   // never seen in the command list; not evidence it did not run
	SearchEndUnverified = "end_unverified" // seen, then dropped from the list before its end was observed
)

// SearchTerminal reports whether a search outcome is no longer followed.
func SearchTerminal(state string) bool {
	switch state {
	case SearchCompleted, SearchFailed, SearchNotObserved, SearchEndUnverified, SearchNotTracked:
		return true
	}
	return false
}

// OutcomeTerminal reports whether an outcome is no longer tracked.
func OutcomeTerminal(state string) bool {
	return state == OutcomeCompleted || state == OutcomeUntracked
}

// RecoveryOutcome is what was observed after a verified recovery.
type RecoveryOutcome struct {
	IntentID      int64      `json:"intent_id"`
	ManagerID     string     `json:"manager_id"`
	Content       []string   `json:"content"`
	OldDownloadID string     `json:"old_download_id"`
	StartedAt     time.Time  `json:"started_at"`
	State         string     `json:"state"`
	GrabbedAt     *time.Time `json:"grabbed_at,omitempty"`
	NewDownloadID string     `json:"new_download_id,omitempty"`
	NewClientID   string     `json:"new_client_id,omitempty"`
	NewNativeID   string     `json:"new_native_id,omitempty"`
	// GrabVerified is the replacement's verified bytes when first seen in
	// the client; progress is measured from it.
	GrabVerified *int64        `json:"grab_verified,omitempty"`
	ProgressAt   *time.Time    `json:"progress_at,omitempty"`
	CompletedAt  *time.Time    `json:"completed_at,omitempty"`
	CheckedAt    time.Time     `json:"checked_at"`
	Note         string        `json:"note,omitempty"`
	Search       SearchOutcome `json:"search"`
}

// SearchOutcome is what was observed of the replacement search: a search
// command for the same content queued at or after Since (the dispatch of the
// recovery's manager request, less a small clock tolerance).
type SearchOutcome struct {
	State string     `json:"state"`
	Since *time.Time `json:"since,omitempty"`
	// AfterID is the highest command ID the manager listed just before the
	// request; only newer commands are attributed. Nil when not read.
	AfterID   *int64     `json:"after_id,omitempty"`
	CommandID *int64     `json:"command_id,omitempty"`
	Name      string     `json:"name,omitempty"`
	QueuedAt  *time.Time `json:"queued_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Note      string     `json:"note,omitempty"`
}

func ptrTime(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: ms(*t), Valid: true}
}

// CreateOutcome starts tracking a recovery's replacement; it is a no-op if
// the intent is already tracked.
func (t *Tx) CreateOutcome(o RecoveryOutcome) error {
	_, err := t.exec(`INSERT OR IGNORE INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at,
		search_state, search_since, search_after_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, o.IntentID, o.ManagerID, strings.Join(o.Content, ","),
		o.OldDownloadID, ms(o.StartedAt), OutcomeAwaitingGrab, ms(o.StartedAt), SearchUnobserved, ptrTime(o.Search.Since), nullInt(o.Search.AfterID))
	return err
}

// SaveOutcome records an updated outcome.
func (t *Tx) SaveOutcome(o RecoveryOutcome) error {
	_, err := t.exec(`UPDATE recovery_outcomes SET state=?, grabbed_at=?, new_download_id=?, new_client_id=?, new_native_id=?, grab_verified=?,
		progress_at=?, completed_at=?, checked_at=?, note=?, search_state=?, search_since=?, search_command_id=?, search_name=?, search_queued_at=?,
		search_ended_at=?, search_checked_at=?, search_note=? WHERE intent_id=?`, o.State, ptrTime(o.GrabbedAt), nullStr(o.NewDownloadID),
		nullStr(o.NewClientID), nullStr(o.NewNativeID), nullInt(o.GrabVerified), ptrTime(o.ProgressAt), ptrTime(o.CompletedAt), ms(o.CheckedAt),
		o.Note, o.Search.State, ptrTime(o.Search.Since), nullInt(o.Search.CommandID), o.Search.Name, ptrTime(o.Search.QueuedAt),
		ptrTime(o.Search.EndedAt), ptrTime(o.Search.CheckedAt), o.Search.Note, o.IntentID)
	return err
}

// SaveSearchOutcome records only an outcome's replacement-search fields, so
// an observation made outside the engine cannot overwrite grab progress the
// engine recorded meanwhile.
func (t *Tx) SaveSearchOutcome(intentID int64, so SearchOutcome) error {
	_, err := t.exec(`UPDATE recovery_outcomes SET search_state=?, search_since=?, search_command_id=?, search_name=?, search_queued_at=?,
		search_ended_at=?, search_checked_at=?, search_note=? WHERE intent_id=?`, so.State, ptrTime(so.Since), nullInt(so.CommandID), so.Name,
		ptrTime(so.QueuedAt), ptrTime(so.EndedAt), ptrTime(so.CheckedAt), so.Note, intentID)
	return err
}

const outcomeCols = `intent_id, manager_id, content, old_download_id, started_at, state, grabbed_at, COALESCE(new_download_id, ''),
	COALESCE(new_client_id, ''), COALESCE(new_native_id, ''), grab_verified, progress_at, completed_at, checked_at, note,
	search_state, search_since, search_command_id, search_name, search_queued_at, search_ended_at, search_checked_at, search_note, search_after_id`

func scanOutcomes(rows *sql.Rows) ([]RecoveryOutcome, error) {
	defer rows.Close()
	var out []RecoveryOutcome
	for rows.Next() {
		var o RecoveryOutcome
		var content string
		var started, checked int64
		var grabbed, gv, prog, done, sSince, sCmd, sQueued, sEnded, sChecked, sAfter sql.NullInt64
		if err := rows.Scan(&o.IntentID, &o.ManagerID, &content, &o.OldDownloadID, &started, &o.State, &grabbed, &o.NewDownloadID,
			&o.NewClientID, &o.NewNativeID, &gv, &prog, &done, &checked, &o.Note,
			&o.Search.State, &sSince, &sCmd, &o.Search.Name, &sQueued, &sEnded, &sChecked, &o.Search.Note, &sAfter); err != nil {
			return nil, err
		}
		o.Search.Since, o.Search.QueuedAt, o.Search.EndedAt, o.Search.CheckedAt = ptrMS(sSince), ptrMS(sQueued), ptrMS(sEnded), ptrMS(sChecked)
		if sCmd.Valid {
			o.Search.CommandID = &sCmd.Int64
		}
		if sAfter.Valid {
			o.Search.AfterID = &sAfter.Int64
		}
		if content != "" {
			o.Content = strings.Split(content, ",")
		}
		o.StartedAt, o.CheckedAt = fromMS(started), fromMS(checked)
		o.GrabbedAt, o.ProgressAt, o.CompletedAt = ptrMS(grabbed), ptrMS(prog), ptrMS(done)
		if gv.Valid {
			o.GrabVerified = &gv.Int64
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OpenOutcomes returns outcomes still being tracked: the download outcome
// is not terminal, or the replacement search is not yet settled.
func (t *Tx) OpenOutcomes() ([]RecoveryOutcome, error) {
	rows, err := t.query("SELECT "+outcomeCols+" FROM recovery_outcomes WHERE state NOT IN (?, ?) OR search_state IN (?, ?, ?) ORDER BY intent_id",
		OutcomeCompleted, OutcomeUntracked, SearchUnobserved, SearchAccepted, SearchRunning)
	if err != nil {
		return nil, err
	}
	return scanOutcomes(rows)
}

// ManagersAwaitingSearch returns the managers with a tracked outcome whose
// replacement search is not yet settled, so the engine reads their command
// lists only while that is useful.
func (s *Store) ManagersAwaitingSearch(ctx context.Context) (map[string]bool, error) {
	rows, err := s.r.QueryContext(ctx, `SELECT DISTINCT manager_id FROM recovery_outcomes WHERE search_state IN (?, ?, ?)`,
		SearchUnobserved, SearchAccepted, SearchRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// Outcomes returns the outcomes of the given intents.
func (s *Store) Outcomes(ctx context.Context, ids []int64) (map[int64]RecoveryOutcome, error) {
	out := map[int64]RecoveryOutcome{}
	if len(ids) == 0 {
		return out, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.r.QueryContext(ctx, "SELECT "+outcomeCols+" FROM recovery_outcomes WHERE intent_id IN ("+ph+")", args...)
	if err != nil {
		return nil, err
	}
	list, err := scanOutcomes(rows)
	for _, o := range list {
		out[o.IntentID] = o
	}
	return out, err
}
