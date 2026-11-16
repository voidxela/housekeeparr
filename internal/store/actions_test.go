package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func testIntent(key string, locks ...string) Intent {
	return Intent{Key: key, Workflow: "no_progress_recovery", Origin: "manual", Subject: json.RawMessage(`{"client_id":"qbit"}`),
		PolicyRevision: "p1", ConfigRevision: "c1", InstanceID: "i", Fingerprint: "f1",
		Steps: []Step{{Op: "manager.queue.remove", Request: json.RawMessage(`{"removeFromClient":false}`)}, {Op: "client.stop"}},
		Locks: locks}
}

func create(t *testing.T, s *Store, in Intent) int64 {
	t.Helper()
	var id int64
	if err := s.Write(ctx, func(tx *Tx) error { var err error; id, err = tx.CreateIntent(in, t0); return err }); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIntentLocksAndIdempotency(t *testing.T) {
	s := openTest(t, t.TempDir())
	id := create(t, s, testIntent("k1", "transfer:qbit|aa", "content:sonarr|episode:1"))

	err := s.Write(ctx, func(tx *Tx) error { _, err := tx.CreateIntent(testIntent("k1"), t0); return err })
	if !errors.Is(err, ErrDuplicateIntent) {
		t.Fatalf("duplicate key: %v", err)
	}
	err = s.Write(ctx, func(tx *Tx) error {
		_, err := tx.CreateIntent(testIntent("k2", "content:sonarr|episode:1"), t0)
		return err
	})
	var held *LockHeldError
	if !errors.As(err, &held) || held.Holder != id {
		t.Fatalf("conflicting lock: %v", err)
	}
	// The failed attempt left nothing behind.
	if all, _ := s.Intents(ctx, IntentFilter{}); len(all) != 1 {
		t.Fatalf("intents after rejected create: %d", len(all))
	}
	in, err := s.Intent(ctx, id)
	if err != nil || in.State != IntentReserved || len(in.Steps) != 2 || len(in.Locks) != 2 || in.Steps[0].State != StepPending {
		t.Fatalf("intent %+v %v", in, err)
	}
}

func TestIntentTransitionsAndLockRelease(t *testing.T) {
	s := openTest(t, t.TempDir())
	id := create(t, s, testIntent("k", "transfer:qbit|aa"))
	w := func(fn func(tx *Tx) error) error { return s.Write(ctx, fn) }

	if err := w(func(tx *Tx) error { return tx.FinishStep(id, 1, StepSucceeded, nil, "", t0) }); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("finishing an undispatched step: %v", err)
	}
	if err := w(func(tx *Tx) error {
		if err := tx.SetIntentState(id, IntentExecuting, "", t0); err != nil {
			return err
		}
		return tx.BeginStep(id, 1, t0)
	}); err != nil {
		t.Fatal(err)
	}
	if err := w(func(tx *Tx) error { return tx.BeginStep(id, 1, t0) }); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("re-dispatching a step: %v", err)
	}
	// Timed out: uncertain keeps the lock and requires review.
	if err := w(func(tx *Tx) error {
		if err := tx.FinishStep(id, 1, StepUncertain, nil, "timeout", t0); err != nil {
			return err
		}
		return tx.SetIntentState(id, IntentUncertain, "outcome unknown", t0)
	}); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.LockHolder(ctx, "transfer:qbit|aa"); h != id {
		t.Fatalf("uncertain intent released its lock (holder %d)", h)
	}
	in, _ := s.Intent(ctx, id)
	if !in.ReviewRequired || in.FinishedAt != nil {
		t.Fatalf("uncertain intent %+v", in)
	}
	if err := w(func(tx *Tx) error { return tx.SetIntentState(id, IntentExecuting, "", t0) }); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("uncertain → executing must be refused: %v", err)
	}
	// Reconciliation settles the step, then review resolves the intent.
	if err := w(func(tx *Tx) error {
		if err := tx.FinishStep(id, 1, StepSucceeded, json.RawMessage(`{"verified":true}`), "", t0); err != nil {
			return err
		}
		if err := tx.FinishStep(id, 2, StepSkipped, nil, "", t0); err != nil {
			return err
		}
		return tx.SetIntentState(id, IntentFailed, "reviewed", t0.Add(time.Minute))
	}); err != nil {
		t.Fatal(err)
	}
	if h, _ := s.LockHolder(ctx, "transfer:qbit|aa"); h != 0 {
		t.Fatalf("terminal intent kept its lock")
	}
	in, _ = s.Intent(ctx, id)
	if in.FinishedAt == nil || in.Steps[0].State != StepSucceeded || string(in.Steps[0].Outcome) != `{"verified":true}` || in.Steps[1].State != StepSkipped {
		t.Fatalf("final intent %+v", in)
	}
}

func TestBudgetsPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	win := t0.Truncate(24 * time.Hour)
	for i := 0; i < 2; i++ {
		if err := s.Write(ctx, func(tx *Tx) error { return tx.ReserveBudget("manager:sonarr:search", win, 2, 1) }); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s = openTest(t, dir)
	err := s.Write(ctx, func(tx *Tx) error { return tx.ReserveBudget("manager:sonarr:search", win, 2, 1) })
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("budget after restart: %v", err)
	}
	if used, _ := s.BudgetUsed(ctx, "manager:sonarr:search", win); used != 2 {
		t.Fatalf("used %d", used)
	}
	// A new window starts fresh.
	if err := s.Write(ctx, func(tx *Tx) error { return tx.ReserveBudget("manager:sonarr:search", win.Add(24*time.Hour), 2, 1) }); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreDetection(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	if st, err := s.CheckRestore(ctx); err != nil || st.Suspected {
		t.Fatalf("fresh store: %+v %v", st, err)
	}
	id := create(t, s, testIntent("k1"))
	if err := s.NoteJournalHighWater(id); err != nil {
		t.Fatal(err)
	}
	backup, err := s.Backup(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	id2 := create(t, s, testIntent("k2"))
	if err := s.NoteJournalHighWater(id2); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.CheckRestore(ctx); st.Suspected {
		t.Fatalf("current database flagged: %+v", st)
	}
	s.Close()

	// Restore the older copy over the live database.
	b, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{dbFile, dbFile + "-wal", dbFile + "-shm"} {
		os.Remove(filepath.Join(dir, f))
	}
	if err := os.WriteFile(filepath.Join(dir, dbFile), b, 0o600); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, dir)
	st, err := s.CheckRestore(ctx)
	if err != nil || !st.Suspected || st.MarkerID != id2 || st.DatabaseMaxID != id {
		t.Fatalf("restored database not detected: %+v %v", st, err)
	}
	if err := s.AcknowledgeRestore(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.CheckRestore(ctx); st.Suspected {
		t.Fatalf("acknowledged restore still suspected: %+v", st)
	}
	// A different database entirely (new instance) is also detected.
	s.Close()
	for _, f := range []string{dbFile, dbFile + "-wal", dbFile + "-shm"} {
		os.Remove(filepath.Join(dir, f))
	}
	s = openTest(t, dir)
	if st, _ := s.CheckRestore(ctx); !st.Suspected {
		t.Fatalf("replaced database not detected: %+v", st)
	}
}

// TestForwardMigrationPreservesV2State builds a database exactly as schema
// version 2 created it, then opens it with this build.
func TestForwardMigrationPreservesV2State(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_init.sql", "0002_queue_match_since.sql"} {
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, q := range []string{
		"INSERT INTO meta(key, value) VALUES('schema_version','2'),('data_origin','live'),('instance_id','v2inst'),('created_at','0')",
		`INSERT INTO findings(dedup_key, type, severity, state, title, summary, subject_json, detail_json, first_seen, last_observed, last_evaluated, evidence_at)
		 VALUES('transfer:qbit|aa|stalled','transfer.stalled','warning','open','v2 finding','s','{"client_id":"qbit","native_id":"aa"}','{}',1,2,3,4)`,
		`INSERT INTO queue_items(manager_id, item_key, first_seen, last_seen, present, latest_json, match_status, match_since)
		 VALUES('sonarr','aa|episode:1',1,2,1,'{"manager_id":"sonarr"}','not_found',7)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("build v2 database: %v", err)
		}
	}
	db.Close()

	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v2 database: %v", err)
	}
	defer s.Close()
	var ver string
	if err := s.r.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&ver); err != nil || ver != strconv.Itoa(SchemaVersion()) {
		t.Fatalf("schema version %q %v", ver, err)
	}
	if fs, err := s.Findings(ctx, FindingFilter{}); err != nil || len(fs) != 1 || fs[0].Title != "v2 finding" {
		t.Fatalf("findings not preserved: %v %+v", err, fs)
	}
	items, err := s.QueueItems(ctx, QueueFilter{})
	if err != nil || len(items) != 1 || items[0].MatchSince == nil {
		t.Fatalf("queue items %+v %v", items, err)
	}
	// The journal works on the migrated database, and a v2 database has
	// no journal marker, so it is not suspected of being restored.
	id := create(t, s, testIntent("k", "transfer:qbit|aa"))
	if in, err := s.Intent(ctx, id); err != nil || in.State != IntentReserved {
		t.Fatalf("journal on migrated database: %+v %v", in, err)
	}
	if st, _ := s.CheckRestore(ctx); st.Suspected {
		t.Fatalf("migrated database suspected: %+v", st)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "backups")); len(entries) != 1 {
		t.Fatalf("expected one pre-migration backup, found %d", len(entries))
	}
}

func TestPruneKeepsUncertainAndActiveActions(t *testing.T) {
	s := openTest(t, t.TempDir())
	done := create(t, s, testIntent("done"))
	held := create(t, s, testIntent("held", "transfer:qbit|bb"))
	active := create(t, s, testIntent("active", "transfer:qbit|cc"))
	err := s.Write(ctx, func(tx *Tx) error {
		if err := tx.SetIntentState(done, IntentCancelled, "x", t0); err != nil {
			return err
		}
		if err := tx.SetIntentState(held, IntentExecuting, "", t0); err != nil {
			return err
		}
		return tx.SetIntentState(held, IntentUncertain, "timeout", t0)
	})
	if err != nil {
		t.Fatal(err)
	}
	var res PruneResult
	if err := s.Write(ctx, func(tx *Tx) error {
		var err error
		res, err = tx.Prune(t0.Add(365*24*time.Hour), Retention{Observations: time.Hour, ConnectionChecks: time.Hour, ResolvedFindings: time.Hour, AuditEvents: time.Hour})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Intents(ctx, IntentFilter{})
	if res.Actions != 1 || len(all) != 2 {
		t.Fatalf("pruned %d, left %d", res.Actions, len(all))
	}
	for _, id := range []int64{held, active} {
		if in, err := s.Intent(ctx, id); err != nil || len(in.Locks) != 1 {
			t.Fatalf("intent %d lost: %+v %v", id, in, err)
		}
	}
	// Pruning never lowers the restore high-water mark.
	if max, _ := s.maxIntentID(ctx); max != active {
		t.Fatalf("high-water mark %d", max)
	}
}

func TestFailedOrCancelledIntentDoesNotBlockRetry(t *testing.T) {
	s := openTest(t, t.TempDir())
	first := create(t, s, testIntent("same", "transfer:qbit|aa"))
	if err := s.Write(ctx, func(tx *Tx) error { return tx.SetIntentState(first, IntentCancelled, "nothing sent", t0) }); err != nil {
		t.Fatal(err)
	}
	second := create(t, s, testIntent("same", "transfer:qbit|aa"))
	if second == first {
		t.Fatal("no new intent")
	}
	if old, _ := s.Intent(ctx, first); old.Key != "same~1" || old.State != IntentCancelled {
		t.Fatalf("history of the first attempt %+v", old)
	}
	// An active (or uncertain) intent still blocks.
	err := s.Write(ctx, func(tx *Tx) error { _, err := tx.CreateIntent(testIntent("same"), t0); return err })
	if !errors.Is(err, ErrDuplicateIntent) {
		t.Fatalf("active intent did not block: %v", err)
	}
}
