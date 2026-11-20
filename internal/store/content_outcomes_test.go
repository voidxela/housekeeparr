package store

import (
	"context"
	"testing"
	"time"
)

// Schema 24 adds per-content outcomes. An outcome recorded under schema 23
// opens with none and keeps working; a new outcome for several content
// items follows each one, and the record survives a round trip.
func TestContentOutcomesUpgradeFrom23(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := buildSchema(t, dir, "0024", 23)
	if _, err := db.Exec(`INSERT INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at)
		VALUES (1, 'sonarr', 'episode:5', 'aa', 1, 'grabbed', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("upgrade from schema 23: %v", err)
	}
	defer s.Close()
	got, err := s.Outcomes(ctx, []int64{1})
	if err != nil || got[1].State != OutcomeGrabbed || len(got[1].PerContent) != 0 {
		t.Fatalf("legacy outcome %+v %v", got[1], err)
	}
	start := time.Unix(100, 0)
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.CreateOutcome(RecoveryOutcome{IntentID: 2, ManagerID: "sonarr", Content: []string{"episode:16", "episode:17"}, OldDownloadID: "bb", StartedAt: start})
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Outcomes(ctx, []int64{2})
	o := got[2]
	if len(o.PerContent) != 2 || o.PerContent[0].Content != "episode:16" || o.PerContent[1].State != OutcomeAwaitingGrab {
		t.Fatalf("new multi-content outcome %+v", o)
	}
	o.PerContent[0].State, o.PerContent[0].NewDownloadID = OutcomeGrabbed, "cc"
	o.CheckedAt = start.Add(time.Minute)
	if err := s.Write(ctx, func(tx *Tx) error { return tx.SaveOutcome(o) }); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Outcomes(ctx, []int64{2})
	if p := got[2].PerContent; len(p) != 2 || p[0].State != OutcomeGrabbed || p[0].NewDownloadID != "cc" {
		t.Fatalf("saved per-content outcome %+v", p)
	}
	// A single-content outcome has no per-content records.
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.CreateOutcome(RecoveryOutcome{IntentID: 3, ManagerID: "sonarr", Content: []string{"episode:20"}, OldDownloadID: "dd", StartedAt: start})
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Outcomes(ctx, []int64{3}); len(got[3].PerContent) != 0 {
		t.Fatalf("single-content outcome %+v", got[3])
	}
}
