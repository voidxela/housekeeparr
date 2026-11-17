package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A database at schema 12 opens at schema 13. An outcome whose intent is
// still retained gets its client from the intent's subject; one whose
// intent was pruned stays without a client and never exempts a transfer
// (a hash alone does not identify one). Existing retained rows were all
// recorded by verified stops. The identity survives a restart.
func TestForwardMigrationV12HandledIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "0013" {
			break
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	for _, q := range []string{
		"INSERT INTO meta(key, value) VALUES('schema_version','12'),('data_origin','live'),('instance_id','v12inst'),('created_at','0')",
		`INSERT INTO action_intents(id, idem_key, workflow, origin, subject_json, state, policy_revision, config_revision, instance_id,
		 fingerprint, evidence_json, plan_json, created_at, updated_at)
		 VALUES(1, 'k1', 'no_progress_recovery', 'automatic', '{"client_id":"qbit","native_id":"AA"}', 'succeeded', 'r', 'c', 'v12inst', 'f', '{}', '{}', 0, 0)`,
		`INSERT INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at)
		 VALUES(1, 'sonarr', 'episode:5', 'AA', 1000, 'awaiting_grab', 1000)`,
		// Intent 2 was pruned by retention: its client is unrecoverable.
		`INSERT INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at)
		 VALUES(2, 'radarr', 'movie:4', 'bb', 1000, 'completed', 1000)`,
		`INSERT INTO retained_transfers(client_id, native_id, intent_id, wanted_bytes, retained_at) VALUES('qbit', 'cc', 3, 10, 1000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("build v12 database: %v", err)
		}
	}
	db.Close()

	check := func(s *Store) {
		t.Helper()
		rec, err := s.RecoveredDownloads(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rec) != 1 || rec[HandledKey("qbit", "aa")] != 1 {
			t.Fatalf("handled transfers %v (want only qbit|aa from intent 1)", rec)
		}
		ret, err := s.RetainedTransfers(ctx, false, 0)
		if err != nil || len(ret) != 1 || ret[0].Disposition != RetainedStopped {
			t.Fatalf("retained %+v %v", ret, err)
		}
	}
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v12 database: %v", err)
	}
	check(s)
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.CreateOutcome(RecoveryOutcome{IntentID: 4, ManagerID: "radarr", Content: []string{"movie:9"}, OldDownloadID: "AA",
			OldClientID: "qbit2", StartedAt: time.Unix(2, 0)})
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rec, _ := s.RecoveredDownloads(ctx)
	if len(rec) != 2 || rec[HandledKey("qbit", "aa")] != 1 || rec[HandledKey("qbit2", "aa")] != 4 {
		t.Fatalf("after restart: %v (the same hash on two clients is two transfers)", rec)
	}
}

// A running record is upgraded to stopped by a later verified stop, never
// the reverse, and its sizes follow fresh listings without unknown values
// replacing known ones.
func TestRetainedDispositionAndRefresh(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	zero, ten := int64(0), int64(10)
	write := func(f func(tx *Tx) error) {
		t.Helper()
		if err := s.Write(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	write(func(tx *Tx) error {
		return tx.RecordRetained(RetainedTransfer{ClientID: "q", NativeID: "a", IntentID: 1, Disposition: RetainedRunning, VerifiedBytes: &zero, RetainedAt: time.Unix(1, 0)})
	})
	if u, _ := s.RetainedUsage(ctx); u.Count != 1 || u.Running != 1 || u.UnknownSize != 1 || u.NoPayload != 0 {
		t.Fatalf("running, unknown size: %+v (a running transfer can obtain payload)", u)
	}
	write(func(tx *Tx) error {
		return tx.RecordRetained(RetainedTransfer{ClientID: "q", NativeID: "a", IntentID: 1, Disposition: RetainedStopped, RetainedAt: time.Unix(2, 0)})
	})
	if u, _ := s.RetainedUsage(ctx); u.Count != 1 || u.Running != 0 || u.NoPayload != 1 {
		t.Fatalf("stopped, no payload: %+v", u)
	}
	write(func(tx *Tx) error {
		return tx.RecordRetained(RetainedTransfer{ClientID: "q", NativeID: "a", IntentID: 1, Disposition: RetainedRunning, RetainedAt: time.Unix(3, 0)})
	})
	write(func(tx *Tx) error { return tx.RefreshRetained("q", "a", &ten, nil) })
	write(func(tx *Tx) error { return tx.RefreshRetained("q", "a", nil, nil) })
	if u, _ := s.RetainedUsage(ctx); u.Running != 0 || u.KnownBytes != 10 || u.NoPayload != 0 {
		t.Fatalf("after refresh: %+v", u)
	}
}
