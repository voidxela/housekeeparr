package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A database at schema 9 (before replacement-search tracking, the search
// baseline, and workflow pauses: schemas 10–12) opens at the current schema; outcomes recorded before are marked not tracked, never
// "not observed", and new outcomes start unobserved with their search
// window.
func TestForwardMigrationV9SearchOutcomesAndPauses(t *testing.T) {
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
		if e.Name() >= "0010" {
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
		"INSERT INTO meta(key, value) VALUES('schema_version','9'),('data_origin','live'),('instance_id','v9inst'),('created_at','0')",
		`INSERT INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at)
		 VALUES(1, 'sonarr', 'episode:5', 'aa', 1000, 'awaiting_grab', 1000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("build v9 database: %v", err)
		}
	}
	db.Close()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v9 database: %v", err)
	}
	defer s.Close()
	got, err := s.Outcomes(ctx, []int64{1})
	if err != nil || got[1].Search.State != SearchNotTracked || got[1].State != OutcomeAwaitingGrab {
		t.Fatalf("pre-existing outcome after upgrade: %+v %v", got[1], err)
	}
	need, err := s.ManagersAwaitingSearch(ctx)
	if err != nil || len(need) != 0 {
		t.Fatalf("an untracked search must not cause command reads: %v %v", need, err)
	}

	since := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	baseline := int64(45)
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.CreateOutcome(RecoveryOutcome{IntentID: 2, ManagerID: "radarr", Content: []string{"movie:4"}, OldDownloadID: "bb",
			StartedAt: since.Add(time.Minute), Search: SearchOutcome{Since: &since, AfterID: &baseline}})
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Outcomes(ctx, []int64{2})
	if o := got[2]; o.Search.State != SearchUnobserved || o.Search.Since == nil || !o.Search.Since.Equal(since) || o.Search.AfterID == nil || *o.Search.AfterID != 45 {
		t.Fatalf("new outcome: %+v", o.Search)
	}
	if need, _ := s.ManagersAwaitingSearch(ctx); !need["radarr"] || len(need) != 1 {
		t.Fatalf("managers awaiting search: %v", need)
	}
	id, ended := int64(46), since.Add(2*time.Minute)
	o := got[2]
	o.Search = SearchOutcome{State: SearchCompleted, Since: &since, AfterID: &baseline, CommandID: &id, Name: "MoviesSearch", EndedAt: &ended, Note: "done"}
	if err := s.Write(ctx, func(tx *Tx) error { return tx.SaveSearchOutcome(2, o.Search) }); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Outcomes(ctx, []int64{2})
	if g := got[2].Search; g.State != SearchCompleted || g.CommandID == nil || *g.CommandID != 46 || g.Name != "MoviesSearch" || g.EndedAt == nil {
		t.Fatalf("saved search outcome: %+v", g)
	}
	if need, _ := s.ManagersAwaitingSearch(ctx); len(need) != 0 {
		t.Fatalf("settled search still awaited: %v", need)
	}
	// Schema 12: no pauses after the upgrade; pause and resume round-trip.
	if ps, err := s.WorkflowPauses(ctx); err != nil || len(ps) != 0 {
		t.Fatalf("pauses after upgrade: %v %v", ps, err)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.PauseWorkflow(WorkflowPause{Workflow: "no_progress_recovery", PausedAt: since, Actor: "cli", Note: "n"})
	}); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.WorkflowPauses(ctx); ps["no_progress_recovery"].Note != "n" {
		t.Fatalf("pause: %v", ps)
	}
	var resumed bool
	if err := s.Write(ctx, func(tx *Tx) error {
		var err error
		resumed, err = tx.ResumeWorkflow("no_progress_recovery")
		return err
	}); err != nil || !resumed {
		t.Fatalf("resume: %v %v", resumed, err)
	}
}
