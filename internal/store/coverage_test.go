package store

import (
	"context"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// A sweep completes only when every listed group was read since it began;
// only a complete group list removes a group; gaps keep their first-seen
// time and leave when filled.
func TestCoverageSweep(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	t0 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ep := func(k string, g int64, wanted, file bool) domain.CoverageItem {
		return domain.CoverageItem{Key: k, GroupID: g, Label: k, Wanted: wanted, HasFile: file}
	}
	write := func(f func(tx *Tx) error) {
		t.Helper()
		if err := s.Write(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	groups := []domain.CoverageGroup{{ID: 1, Title: "A", Monitored: true}, {ID: 2, Title: "B", Monitored: true}}
	write(func(tx *Tx) error { return tx.StartCoverageSweep("sonarr", groups, t0) })
	due, _ := s.CoverageGroupsDue(ctx, "sonarr", t0, 10)
	if len(due) != 2 {
		t.Fatalf("due %+v", due)
	}
	var done bool
	write(func(tx *Tx) error {
		if err := tx.SaveCoverageGroup("sonarr", 1, []domain.CoverageItem{ep("episode:1", 1, true, true), ep("episode:2", 1, true, false), ep("episode:3", 1, false, false)}, t0); err != nil {
			return err
		}
		var err error
		done, err = tx.FinishCoverageSweepIfDone("sonarr", t0)
		return err
	})
	if done {
		t.Fatal("finished with a group unread")
	}
	tot, _ := s.CoverageTotals(ctx)
	if len(tot) != 1 || tot[0].Wanted != 2 || tot[0].Have != 1 || tot[0].Groups != 2 || tot[0].GroupsRead != 1 || tot[0].Gaps != 1 {
		t.Fatalf("totals %+v", tot)
	}
	t1 := t0.Add(time.Minute)
	write(func(tx *Tx) error {
		if err := tx.SaveCoverageGroup("sonarr", 2, []domain.CoverageItem{ep("episode:4", 2, true, false)}, t1); err != nil {
			return err
		}
		var err error
		done, err = tx.FinishCoverageSweepIfDone("sonarr", t1)
		return err
	})
	if !done {
		t.Fatal("sweep not finished after every group was read")
	}
	if days, _ := s.CoverageTrend(ctx, "sonarr", 30, t1); len(days) != 1 || days[0].Wanted != 3 || days[0].Have != 1 {
		t.Fatalf("trend %+v", days)
	}
	// Next sweep: group 2 no longer listed (series removed); episode 2 filled.
	t2 := t1.Add(6 * time.Hour)
	write(func(tx *Tx) error { return tx.StartCoverageSweep("sonarr", groups[:1], t2) })
	write(func(tx *Tx) error {
		return tx.SaveCoverageGroup("sonarr", 1, []domain.CoverageItem{ep("episode:1", 1, true, true), ep("episode:2", 1, true, true)}, t2)
	})
	tot, _ = s.CoverageTotals(ctx)
	if tot[0].Wanted != 2 || tot[0].Have != 2 || tot[0].Gaps != 0 || tot[0].Groups != 1 {
		t.Fatalf("after the second sweep %+v", tot)
	}
	// Gap first-seen is kept across reads.
	write(func(tx *Tx) error {
		return tx.SaveCoverageGroup("sonarr", 1, []domain.CoverageItem{ep("episode:5", 1, true, false)}, t2)
	})
	write(func(tx *Tx) error {
		return tx.SaveCoverageGroup("sonarr", 1, []domain.CoverageItem{ep("episode:5", 1, true, false)}, t2.Add(time.Hour))
	})
	if gs, _ := s.CoverageGaps(ctx, "sonarr", 0, 10); len(gs) != 1 || !gs[0].FirstSeen.Equal(t2) || !gs[0].LastSeen.Equal(t2.Add(time.Hour)) {
		t.Fatalf("gaps %+v", gs)
	}
}

// Schema 25 adds coverage tables to a schema-24 database without touching
// existing data.
func TestCoverageUpgradeFrom24(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := buildSchema(t, dir, "0025", 24)
	if _, err := db.Exec(`INSERT INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at, content_outcomes)
		VALUES (1, 'sonarr', 'episode:5', 'aa', 1, 'grabbed', 1, '')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("upgrade from schema 24: %v", err)
	}
	defer s.Close()
	if tot, err := s.CoverageTotals(ctx); err != nil || len(tot) != 0 {
		t.Fatalf("coverage after upgrade %+v %v", tot, err)
	}
	if oc, err := s.Outcomes(ctx, []int64{1}); err != nil || oc[1].State != OutcomeGrabbed {
		t.Fatalf("existing outcome %+v %v", oc, err)
	}
}
