package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// IntentState is an action intent's lifecycle state (SPEC §10).
type IntentState string

const (
	IntentProposed    IntentState = "proposed"
	IntentReserved    IntentState = "reserved"
	IntentExecuting   IntentState = "executing"
	IntentReconciling IntentState = "reconciling"
	IntentSucceeded   IntentState = "succeeded"
	IntentFailed      IntentState = "failed"
	IntentUncertain   IntentState = "uncertain"
	IntentCancelled   IntentState = "cancelled"
)

// Terminal reports whether the intent is finished. Uncertain is not
// terminal: it holds its locks until reviewed.
func (s IntentState) Terminal() bool {
	return s == IntentSucceeded || s == IntentFailed || s == IntentCancelled
}

// StepState is one journaled step's state.
type StepState string

const (
	StepPending StepState = "pending"
	// StepDispatching is committed immediately before the external request
	// is sent. Found after a crash, it means the outcome is unknown.
	StepDispatching StepState = "dispatching"
	StepSucceeded   StepState = "succeeded"
	StepFailed      StepState = "failed"
	StepUncertain   StepState = "uncertain"
	StepSkipped     StepState = "skipped"
)

// Intent is a durable action intent with its steps and locks.
type Intent struct {
	ID             int64           `json:"id"`
	Key            string          `json:"key"`
	Workflow       string          `json:"workflow"`
	Origin         string          `json:"origin"`
	FindingID      *int64          `json:"finding_id,omitempty"`
	Subject        json.RawMessage `json:"subject"`
	State          IntentState     `json:"state"`
	PolicyRevision string          `json:"policy_revision"`
	ConfigRevision string          `json:"config_revision"`
	InstanceID     string          `json:"instance_id"`
	Fingerprint    string          `json:"fingerprint"`
	Evidence       json.RawMessage `json:"evidence"`
	Plan           json.RawMessage `json:"plan"`
	Explanation    string          `json:"explanation"`
	ReviewRequired bool            `json:"review_required"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	FinishedAt     *time.Time      `json:"finished_at,omitempty"`
	Steps          []Step          `json:"steps"`
	Locks          []string        `json:"locks"`
}

// Step is one external operation of an intent.
type Step struct {
	IntentID     int64           `json:"intent_id"`
	Seq          int             `json:"seq"`
	Op           string          `json:"op"`
	Target       json.RawMessage `json:"target"`
	Request      json.RawMessage `json:"request"`
	Expect       json.RawMessage `json:"expect"`
	State        StepState       `json:"state"`
	Attempts     int             `json:"attempts"`
	DispatchedAt *time.Time      `json:"dispatched_at,omitempty"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
	Outcome      json.RawMessage `json:"outcome,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// LockHeldError reports a subject already locked by another intent.
type LockHeldError struct {
	Subject string
	Holder  int64
}

func (e *LockHeldError) Error() string {
	return fmt.Sprintf("%s is held by action #%d", e.Subject, e.Holder)
}

var (
	// ErrDuplicateIntent means an intent with the same idempotency key exists.
	ErrDuplicateIntent = errors.New("an action with this idempotency key already exists")
	// ErrBudgetExhausted means a budget reservation would exceed its limit.
	ErrBudgetExhausted = errors.New("budget exhausted")
	// ErrIllegalTransition rejects a state change the journal does not allow.
	ErrIllegalTransition = errors.New("illegal action state transition")
)

func rawOrEmpty(b json.RawMessage) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

// CreateIntent journals a reserved intent, its pending steps, and its locks.
// It fails without side effects if the key exists or any lock is held.
func (t *Tx) CreateIntent(in Intent, now time.Time) (int64, error) {
	var prevID int64
	var prevState string
	err := t.queryRow("SELECT id, state FROM action_intents WHERE idem_key=?", in.Key).Scan(&prevID, &prevState)
	switch {
	case err == nil && (IntentState(prevState) == IntentFailed || IntentState(prevState) == IntentCancelled):
		// Nothing of a failed or cancelled attempt took effect, so it does
		// not block a new one (cooldowns and budgets govern repetition).
		// Keep its history under a retired key.
		if _, err := t.exec("UPDATE action_intents SET idem_key=? WHERE id=?", fmt.Sprintf("%s~%d", in.Key, prevID), prevID); err != nil {
			return 0, err
		}
	case err == nil:
		return 0, ErrDuplicateIntent
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}
	for _, sub := range in.Locks {
		var holder int64
		err := t.queryRow("SELECT intent_id FROM action_locks WHERE subject=?", sub).Scan(&holder)
		if err == nil {
			return 0, &LockHeldError{Subject: sub, Holder: holder}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	var fid sql.NullInt64
	if in.FindingID != nil {
		fid = sql.NullInt64{Int64: *in.FindingID, Valid: true}
	}
	res, err := t.exec(`INSERT INTO action_intents(idem_key, workflow, origin, finding_id, subject_json, state, policy_revision,
		config_revision, instance_id, fingerprint, evidence_json, plan_json, explanation, review_required, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		in.Key, in.Workflow, in.Origin, fid, rawOrEmpty(in.Subject), string(IntentReserved), in.PolicyRevision,
		in.ConfigRevision, in.InstanceID, in.Fingerprint, rawOrEmpty(in.Evidence), rawOrEmpty(in.Plan), in.Explanation, ms(now), ms(now))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for i, st := range in.Steps {
		if _, err := t.exec(`INSERT INTO action_steps(intent_id, seq, op, target_json, request_json, expect_json, state)
			VALUES(?, ?, ?, ?, ?, ?, ?)`, id, i+1, st.Op, rawOrEmpty(st.Target), rawOrEmpty(st.Request), rawOrEmpty(st.Expect), string(StepPending)); err != nil {
			return 0, err
		}
	}
	for _, sub := range in.Locks {
		if _, err := t.exec("INSERT INTO action_locks(subject, intent_id, acquired_at) VALUES(?, ?, ?)", sub, id, ms(now)); err != nil {
			return 0, err
		}
	}
	return id, nil
}

var intentTransitions = map[IntentState][]IntentState{
	IntentReserved:    {IntentExecuting, IntentCancelled, IntentReconciling},
	IntentExecuting:   {IntentSucceeded, IntentFailed, IntentUncertain, IntentReconciling},
	IntentReconciling: {IntentSucceeded, IntentFailed, IntentUncertain},
	// Only an explicit review resolves an uncertain intent.
	IntentUncertain: {IntentSucceeded, IntentFailed, IntentCancelled},
}

// SetIntentState moves an intent to a new state. Terminal states release
// the intent's locks; uncertain keeps them and marks it for review.
func (t *Tx) SetIntentState(id int64, to IntentState, explanation string, now time.Time) error {
	var from string
	if err := t.queryRow("SELECT state FROM action_intents WHERE id=?", id).Scan(&from); err != nil {
		return fmt.Errorf("action #%d: %w", id, err)
	}
	ok := false
	for _, s := range intentTransitions[IntentState(from)] {
		ok = ok || s == to
	}
	if !ok {
		return fmt.Errorf("%w: action #%d %s → %s", ErrIllegalTransition, id, from, to)
	}
	var fin sql.NullInt64
	if to.Terminal() {
		fin = sql.NullInt64{Int64: ms(now), Valid: true}
	}
	review := 0
	if to == IntentUncertain {
		review = 1
	}
	if _, err := t.exec(`UPDATE action_intents SET state=?, explanation=CASE WHEN ?='' THEN explanation ELSE ? END,
		review_required=?, updated_at=?, finished_at=? WHERE id=?`, string(to), explanation, explanation, review, ms(now), fin, id); err != nil {
		return err
	}
	if to.Terminal() {
		_, err := t.exec("DELETE FROM action_locks WHERE intent_id=?", id)
		return err
	}
	return nil
}

// BeginStep records, before the external request is sent, that the step is
// being dispatched. The caller must commit this before sending.
func (t *Tx) BeginStep(id int64, seq int, now time.Time) error {
	res, err := t.exec(`UPDATE action_steps SET state=?, attempts=attempts+1, dispatched_at=? WHERE intent_id=? AND seq=? AND state=?`,
		string(StepDispatching), ms(now), id, seq, string(StepPending))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: step %d of action #%d is not pending", ErrIllegalTransition, seq, id)
	}
	return nil
}

// FinishStep records a step's observed outcome. A pending step may be
// finished directly only as skipped (never dispatched).
func (t *Tx) FinishStep(id int64, seq int, to StepState, outcome json.RawMessage, errText string, now time.Time) error {
	from := StepDispatching
	switch to {
	case StepSkipped:
		from = StepPending
	case StepSucceeded, StepFailed, StepUncertain:
	default:
		return fmt.Errorf("%w: step cannot finish as %s", ErrIllegalTransition, to)
	}
	var out sql.NullString
	if len(outcome) > 0 {
		out = sql.NullString{String: string(outcome), Valid: true}
	}
	// An uncertain step may later be settled by reconciliation.
	res, err := t.exec(`UPDATE action_steps SET state=?, finished_at=?, outcome_json=COALESCE(?, outcome_json), error=? WHERE intent_id=? AND seq=? AND state IN (?, ?)`,
		string(to), ms(now), out, nullStr(errText), id, seq, string(from), string(StepUncertain))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: step %d of action #%d cannot become %s", ErrIllegalTransition, seq, id, to)
	}
	return nil
}

// ReserveBudget consumes n units of scope's budget for the window starting
// at windowStart, failing with ErrBudgetExhausted beyond limit.
func (t *Tx) ReserveBudget(scope string, windowStart time.Time, limit, n int) error {
	var used int
	err := t.queryRow("SELECT used FROM action_budgets WHERE scope=? AND window_start=?", scope, ms(windowStart)).Scan(&used)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if used+n > limit {
		return fmt.Errorf("%w: %s used %d of %d", ErrBudgetExhausted, scope, used, limit)
	}
	_, err = t.exec(`INSERT INTO action_budgets(scope, window_start, used) VALUES(?, ?, ?)
		ON CONFLICT(scope, window_start) DO UPDATE SET used=used+excluded.used`, scope, ms(windowStart), n)
	return err
}

// SetCooldown records that subject must not be acted on before until.
func (t *Tx) SetCooldown(subject string, until time.Time, reason string) error {
	_, err := t.exec(`INSERT INTO action_cooldowns(subject, until, reason) VALUES(?, ?, ?)
		ON CONFLICT(subject) DO UPDATE SET until=excluded.until, reason=excluded.reason`, subject, ms(until), reason)
	return err
}

// BudgetUsed returns consumption for scope in the window.
func (s *Store) BudgetUsed(ctx context.Context, scope string, windowStart time.Time) (int, error) {
	var used int
	err := s.r.QueryRowContext(ctx, "SELECT used FROM action_budgets WHERE scope=? AND window_start=?", scope, ms(windowStart)).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return used, err
}

// Cooldown returns the cooldown for subject, if one is recorded.
func (s *Store) Cooldown(ctx context.Context, subject string) (time.Time, string, bool, error) {
	var until int64
	var reason string
	err := s.r.QueryRowContext(ctx, "SELECT until, reason FROM action_cooldowns WHERE subject=?", subject).Scan(&until, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, "", false, nil
	}
	return fromMS(until), reason, err == nil, err
}

// CooldownRecord is a recorded cooldown.
type CooldownRecord struct {
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
}

// HeldLocks returns every held lock subject with its intent. The table is
// bounded by the number of unfinished intents.
func (s *Store) HeldLocks(ctx context.Context) (map[string]int64, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT subject, intent_id FROM action_locks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var sub string
		var id int64
		if err := rows.Scan(&sub, &id); err != nil {
			return nil, err
		}
		out[sub] = id
	}
	return out, rows.Err()
}

// ActiveCooldowns returns every cooldown that has not expired at now.
func (s *Store) ActiveCooldowns(ctx context.Context, now time.Time) (map[string]CooldownRecord, error) {
	rows, err := s.r.QueryContext(ctx, "SELECT subject, until, reason FROM action_cooldowns WHERE until > ?", ms(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]CooldownRecord{}
	for rows.Next() {
		var sub, reason string
		var until int64
		if err := rows.Scan(&sub, &until, &reason); err != nil {
			return nil, err
		}
		out[sub] = CooldownRecord{Until: fromMS(until), Reason: reason}
	}
	return out, rows.Err()
}

// LockHolder returns the intent holding subject, or 0.
func (s *Store) LockHolder(ctx context.Context, subject string) (int64, error) {
	var id int64
	err := s.r.QueryRowContext(ctx, "SELECT intent_id FROM action_locks WHERE subject=?", subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// IntentFilter selects intents.
type IntentFilter struct {
	ID        int64
	States    []IntentState
	FindingID int64
	Limit     int
}

// Intents lists intents, newest first, with their steps and locks.
func (s *Store) Intents(ctx context.Context, f IntentFilter) ([]Intent, error) {
	q := `SELECT id, idem_key, workflow, origin, finding_id, subject_json, state, policy_revision, config_revision, instance_id,
		fingerprint, evidence_json, plan_json, explanation, review_required, created_at, updated_at, finished_at FROM action_intents`
	var where []string
	var args []any
	if len(f.States) > 0 {
		var ph []string
		for _, st := range f.States {
			ph = append(ph, "?")
			args = append(args, string(st))
		}
		where = append(where, "state IN ("+strings.Join(ph, ",")+")")
	}
	if f.ID != 0 {
		where = append(where, "id=?")
		args = append(args, f.ID)
	}
	if f.FindingID != 0 {
		where = append(where, "finding_id=?")
		args = append(args, f.FindingID)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC"
	if f.Limit > 0 {
		q += " LIMIT " + strconv.Itoa(f.Limit)
	}
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var out []Intent
	for rows.Next() {
		var in Intent
		var fid, fin sql.NullInt64
		var subj, ev, plan, state string
		var created, updated int64
		var review int
		if err := rows.Scan(&in.ID, &in.Key, &in.Workflow, &in.Origin, &fid, &subj, &state, &in.PolicyRevision, &in.ConfigRevision,
			&in.InstanceID, &in.Fingerprint, &ev, &plan, &in.Explanation, &review, &created, &updated, &fin); err != nil {
			rows.Close()
			return nil, err
		}
		if fid.Valid {
			v := fid.Int64
			in.FindingID = &v
		}
		in.Subject, in.Evidence, in.Plan = json.RawMessage(subj), json.RawMessage(ev), json.RawMessage(plan)
		in.State, in.ReviewRequired = IntentState(state), review != 0
		in.CreatedAt, in.UpdatedAt, in.FinishedAt = fromMS(created), fromMS(updated), ptrMS(fin)
		out = append(out, in)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.loadIntentDetail(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Intent returns one intent by id.
func (s *Store) Intent(ctx context.Context, id int64) (Intent, error) {
	all, err := s.Intents(ctx, IntentFilter{ID: id})
	if err != nil {
		return Intent{}, err
	}
	if len(all) == 0 {
		return Intent{}, fmt.Errorf("action #%d: %w", id, sql.ErrNoRows)
	}
	return all[0], nil
}

func (s *Store) loadIntentDetail(ctx context.Context, in *Intent) error {
	rows, err := s.r.QueryContext(ctx, `SELECT seq, op, target_json, request_json, expect_json, state, attempts, dispatched_at,
		finished_at, outcome_json, error FROM action_steps WHERE intent_id=? ORDER BY seq`, in.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		st := Step{IntentID: in.ID}
		var tgt, req, exp, state string
		var disp, fin sql.NullInt64
		var out, errText sql.NullString
		if err := rows.Scan(&st.Seq, &st.Op, &tgt, &req, &exp, &state, &st.Attempts, &disp, &fin, &out, &errText); err != nil {
			rows.Close()
			return err
		}
		st.Target, st.Request, st.Expect = json.RawMessage(tgt), json.RawMessage(req), json.RawMessage(exp)
		st.State, st.DispatchedAt, st.FinishedAt, st.Error = StepState(state), ptrMS(disp), ptrMS(fin), errText.String
		if out.Valid {
			st.Outcome = json.RawMessage(out.String)
		}
		in.Steps = append(in.Steps, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	lrows, err := s.r.QueryContext(ctx, "SELECT subject FROM action_locks WHERE intent_id=? ORDER BY subject", in.ID)
	if err != nil {
		return err
	}
	defer lrows.Close()
	for lrows.Next() {
		var sub string
		if err := lrows.Scan(&sub); err != nil {
			return err
		}
		in.Locks = append(in.Locks, sub)
	}
	return lrows.Err()
}

// --- restore detection ------------------------------------------------------
//
// A database restored from a backup may predate actions that were already
// sent. The journal high-water mark (the highest intent id ever created) is
// also written to a small file next to the database after every intent is
// journaled. If the file records a higher id than the database holds, or a
// different instance, the database was restored or replaced, and writes
// must stay disabled until an operator acknowledges it.

const hwmFile = "journal.hwm"

// RestoreStatus describes whether the database may predate sent actions.
type RestoreStatus struct {
	Suspected     bool   `json:"suspected"`
	Reason        string `json:"reason,omitempty"`
	MarkerID      int64  `json:"marker_id"`
	DatabaseMaxID int64  `json:"database_max_id"`
}

func (s *Store) maxIntentID(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	// sqlite_sequence survives deletion of rows, so pruning old intents
	// never lowers the high-water mark.
	err := s.r.QueryRowContext(ctx, "SELECT seq FROM sqlite_sequence WHERE name='action_intents'").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id.Int64, err
}

// NoteJournalHighWater durably records id as the highest journaled intent.
func (s *Store) NoteJournalHighWater(id int64) error {
	if s.w == nil {
		return ErrReadOnly
	}
	path := filepath.Join(s.dir, hwmFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%s %d\n", s.instance, id); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(s.dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// CheckRestore compares the high-water marker with the database.
func (s *Store) CheckRestore(ctx context.Context) (RestoreStatus, error) {
	dbMax, err := s.maxIntentID(ctx)
	if err != nil {
		return RestoreStatus{}, err
	}
	st := RestoreStatus{DatabaseMaxID: dbMax}
	b, err := os.ReadFile(filepath.Join(s.dir, hwmFile))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	var inst string
	var id int64
	if _, err := fmt.Sscanf(string(b), "%s %d", &inst, &id); err != nil {
		st.Suspected, st.Reason = true, "the journal marker is unreadable"
		return st, nil
	}
	st.MarkerID = id
	switch {
	case inst != s.instance:
		st.Suspected, st.Reason = true, "the journal marker belongs to a different state database (instance "+inst+")"
	case id > dbMax:
		st.Suspected, st.Reason = true, fmt.Sprintf("actions up to #%d were journaled, but this database only knows up to #%d: it was restored from an older copy", id, dbMax)
	}
	return st, nil
}

// AcknowledgeRestore accepts the current database as authoritative after an
// operator has reviewed external state, and records that in the audit log.
func (s *Store) AcknowledgeRestore(ctx context.Context, actor string) error {
	st, err := s.CheckRestore(ctx)
	if err != nil {
		return err
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.Audit(s.now(), actor, "actions.restore_acknowledged", st.Reason)
	}); err != nil {
		return err
	}
	return s.NoteJournalHighWater(st.DatabaseMaxID)
}
