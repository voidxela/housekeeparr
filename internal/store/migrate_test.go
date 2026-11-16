package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestForwardMigrationPreservesV1State builds a database exactly as schema
// version 1 (commit 7adcfed) created it, then opens it with this build.
func TestForwardMigrationPreservesV1State(t *testing.T) {
	dir := t.TempDir()
	v1, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{string(v1),
		"INSERT INTO meta(key, value) VALUES('schema_version','1'),('data_origin','live'),('instance_id','abc123'),('created_at','0')",
		`INSERT INTO findings(dedup_key, type, severity, state, title, summary, subject_json, detail_json, first_seen, last_observed, last_evaluated, evidence_at)
		 VALUES('transfer:qbit|aa|stalled','transfer.stalled','warning','open','old finding','s','{"client_id":"qbit","native_id":"aa"}','{}',1,2,3,4)`,
		`INSERT INTO queue_items(manager_id, item_key, first_seen, last_seen, present, latest_json, match_status)
		 VALUES('sonarr','aa|episode:1',1,2,1,'{"manager_id":"sonarr","title":"x"}','not_found')`,
		`INSERT INTO transfer_observations(client_id, native_id, at, run_id, mono_ms, state_class, sample_json)
		 VALUES('qbit','aa',5,'r',0,'downloading','{}')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v1 database: %v", err)
		}
	}
	db.Close()

	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v1 database: %v", err)
	}
	defer s.Close()
	var ver string
	if err := s.r.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&ver); err != nil || ver != strconv.Itoa(SchemaVersion()) {
		t.Fatalf("schema version %q %v", ver, err)
	}
	if s.InstanceID() != "abc123" {
		t.Fatalf("instance id changed: %s", s.InstanceID())
	}
	fs, err := s.Findings(ctx, FindingFilter{})
	if err != nil || len(fs) != 1 || fs[0].Title != "old finding" {
		t.Fatalf("findings not preserved: %v %+v", err, fs)
	}
	var n int
	if err := s.r.QueryRow("SELECT count(*) FROM transfer_observations").Scan(&n); err != nil || n != 1 {
		t.Fatalf("observations not preserved: %d %v", n, err)
	}
	// The new column works on migrated rows.
	var since time.Time
	err = s.Write(ctx, func(tx *Tx) error {
		since, err = tx.SetQueueMatch("sonarr", "aa|episode:1", MatchNotFound, "qbit", "", "n", time.UnixMilli(1000))
		return err
	})
	if err != nil || !since.Equal(time.UnixMilli(1000).UTC()) {
		t.Fatalf("match_since on migrated row: %v %v", since, err)
	}
	items, err := s.QueueItems(ctx, QueueFilter{})
	if err != nil || len(items) != 1 || items[0].MatchSince == nil {
		t.Fatalf("queue items %+v %v", items, err)
	}
	// A backup of the pre-migration database was written.
	entries, _ := os.ReadDir(filepath.Join(dir, "backups"))
	if len(entries) != 1 {
		t.Fatalf("expected one pre-migration backup, found %d", len(entries))
	}
}
