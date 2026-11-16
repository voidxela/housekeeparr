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
	GrabVerified *int64     `json:"grab_verified,omitempty"`
	ProgressAt   *time.Time `json:"progress_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	CheckedAt    time.Time  `json:"checked_at"`
	Note         string     `json:"note,omitempty"`
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
	_, err := t.exec(`INSERT OR IGNORE INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, o.IntentID, o.ManagerID, strings.Join(o.Content, ","), o.OldDownloadID, ms(o.StartedAt), OutcomeAwaitingGrab, ms(o.StartedAt))
	return err
}

// SaveOutcome records an updated outcome.
func (t *Tx) SaveOutcome(o RecoveryOutcome) error {
	_, err := t.exec(`UPDATE recovery_outcomes SET state=?, grabbed_at=?, new_download_id=?, new_client_id=?, new_native_id=?, grab_verified=?,
		progress_at=?, completed_at=?, checked_at=?, note=? WHERE intent_id=?`, o.State, ptrTime(o.GrabbedAt), nullStr(o.NewDownloadID),
		nullStr(o.NewClientID), nullStr(o.NewNativeID), nullInt(o.GrabVerified), ptrTime(o.ProgressAt), ptrTime(o.CompletedAt), ms(o.CheckedAt),
		o.Note, o.IntentID)
	return err
}

const outcomeCols = `intent_id, manager_id, content, old_download_id, started_at, state, grabbed_at, COALESCE(new_download_id, ''),
	COALESCE(new_client_id, ''), COALESCE(new_native_id, ''), grab_verified, progress_at, completed_at, checked_at, note`

func scanOutcomes(rows *sql.Rows) ([]RecoveryOutcome, error) {
	defer rows.Close()
	var out []RecoveryOutcome
	for rows.Next() {
		var o RecoveryOutcome
		var content string
		var started, checked int64
		var grabbed, gv, prog, done sql.NullInt64
		if err := rows.Scan(&o.IntentID, &o.ManagerID, &content, &o.OldDownloadID, &started, &o.State, &grabbed, &o.NewDownloadID,
			&o.NewClientID, &o.NewNativeID, &gv, &prog, &done, &checked, &o.Note); err != nil {
			return nil, err
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

// OpenOutcomes returns outcomes still being tracked.
func (t *Tx) OpenOutcomes() ([]RecoveryOutcome, error) {
	rows, err := t.query("SELECT "+outcomeCols+" FROM recovery_outcomes WHERE state NOT IN (?, ?) ORDER BY intent_id", OutcomeCompleted, OutcomeUntracked)
	if err != nil {
		return nil, err
	}
	return scanOutcomes(rows)
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
