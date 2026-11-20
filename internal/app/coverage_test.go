package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Every gap gets exactly one primary state, in priority order, with the
// other facts kept as reasons.
func TestExplainGapPrimaryState(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ago := func(h int) *time.Time { x := now.Add(-time.Duration(h) * time.Hour); return &x }
	gap := store.CoverageGap{ManagerID: "sonarr", ContentKey: "episode:5", Label: "Show S01E05"}
	row := func(attention string, matched bool) store.QueueRecord {
		q := store.QueueRecord{ManagerID: "sonarr", Present: true, LastSeen: now, AttentionState: attention,
			Latest: domain.QueueItem{ContentKind: "episode", EpisodeID: 5, Title: "Show.S01E05", TrackedState: domain.KnownValue("downloading")}}
		if matched {
			q.MatchStatus, q.MatchClientID, q.MatchNativeID = store.MatchMatched, "qbit", "aa"
		}
		return q
	}
	searched := store.MissingRecord{ContentKey: "episode:5", FirstSeen: *ago(48), Searches: 2, LastSearchAt: ago(5), State: store.MissingNoResult, NextSearchAt: ago(-3)}
	fresh := store.MissingRecord{ContentKey: "episode:5", FirstSeen: *ago(48)}
	stalled := map[string]bool{"qbit|aa": true}
	for name, tc := range map[string]struct {
		rows   []store.QueueRecord
		m      store.MissingRecord
		grabs  map[string]time.Time
		want   string
		reason string
	}{
		"import pending wins":          {[]store.QueueRecord{row("importBlocked", true)}, searched, nil, GapImportPending, "searched 2 time(s)"},
		"stalled":                      {[]store.QueueRecord{row("", true)}, fresh, nil, GapStalled, "queued"},
		"queued":                       {[]store.QueueRecord{row("", false)}, fresh, nil, GapQueued, ""},
		"searched without a grab":      {nil, searched, nil, GapSearchedNoGrab, "next search not before"},
		"grab after the search is not": {nil, store.MissingRecord{ContentKey: "episode:5", FirstSeen: *ago(48), Searches: 1, LastSearchAt: ago(5), NextSearchAt: ago(-3)}, map[string]time.Time{"episode:5": *ago(4)}, GapDeferred, ""},
		"never searched":               {nil, fresh, nil, GapNoSearchObserved, ""},
		"no evidence":                  {nil, store.MissingRecord{}, nil, GapUnknown, "no evidence"},
		"grabbed earlier, gone":        {nil, store.MissingRecord{}, map[string]time.Time{"episode:5": *ago(30)}, GapUnknown, "no longer queued"},
	} {
		x := explainGap(gap, tc.rows, tc.m, tc.grabs, stalled, now)
		if x.State != tc.want || (tc.reason != "" && !strings.Contains(strings.Join(x.Reasons, "; "), tc.reason)) {
			t.Errorf("%s: %s %v, want %s with %q", name, x.State, x.Reasons, tc.want, tc.reason)
		}
	}
}

// The view counts every gap once, reports an incomplete sweep as such, and
// shows the denominator change between recorded days.
func TestCoverageView(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	day1 := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	items := func(n, files int) []domain.CoverageItem {
		var out []domain.CoverageItem
		for i := 0; i < n; i++ {
			out = append(out, domain.CoverageItem{Key: "episode:" + string(rune('a'+i)), GroupID: 1, Label: "x", Wanted: true, HasFile: i < files})
		}
		return out
	}
	sweep := func(at time.Time, groups []domain.CoverageGroup, its []domain.CoverageItem) {
		t.Helper()
		if err := st.Write(ctx, func(tx *store.Tx) error {
			if err := tx.StartCoverageSweep("sonarr", groups, at); err != nil {
				return err
			}
			if err := tx.SaveCoverageGroup("sonarr", 1, its, at); err != nil {
				return err
			}
			_, err := tx.FinishCoverageSweepIfDone("sonarr", at)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	one := []domain.CoverageGroup{{ID: 1, Title: "Show", Monitored: true}}
	sweep(day1, one, items(10, 6))
	day2 := day1.Add(24 * time.Hour)
	sweep(day2, one, items(12, 7))
	cfg, _, _ := config.Parse([]byte("version = 1\n"), "/x/h.toml")
	svc := New(st, cfg, func() time.Time { return day2 }, nil)
	cv, err := svc.Coverage(ctx, "", 0, 3)
	if err != nil || len(cv.Managers) != 1 {
		t.Fatalf("%+v %v", cv, err)
	}
	m := cv.Managers[0]
	total := 0
	for _, n := range m.States {
		total += n
	}
	if m.Wanted != 12 || m.Have != 7 || m.GapsTotal != 5 || total != 5 || len(m.Gaps) != 3 || !m.Complete {
		t.Fatalf("view %+v", m)
	}
	if m.DenominatorChange == nil || *m.DenominatorChange != 2 || len(m.Trend) != 2 {
		t.Fatalf("trend %+v change %v", m.Trend, m.DenominatorChange)
	}
	// A new sweep with a group not read yet is reported incomplete.
	sweep(day2.Add(7*time.Hour), append(one, domain.CoverageGroup{ID: 2, Title: "New", Monitored: true}), items(12, 7))
	if cv, _ := svc.Coverage(ctx, "", 0, 3); cv.Managers[0].Complete {
		t.Fatal("a sweep with an unread group reported complete")
	}
}

// BenchmarkCoverageView1500 renders the coverage view of 1,500 wanted
// episodes in 50 series, a third of them gaps, from local state only.
func BenchmarkCoverageView1500(b *testing.B) {
	ctx := context.Background()
	st, err := store.Open(ctx, b.TempDir(), store.Options{})
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var groups []domain.CoverageGroup
	for g := int64(1); g <= 50; g++ {
		groups = append(groups, domain.CoverageGroup{ID: g, Title: "S", Monitored: true})
	}
	if err := st.Write(ctx, func(tx *store.Tx) error {
		if err := tx.StartCoverageSweep("sonarr", groups, now); err != nil {
			return err
		}
		for _, g := range groups {
			var items []domain.CoverageItem
			for e := int64(0); e < 30; e++ {
				aired := now.Add(-time.Duration(e) * 24 * time.Hour)
				items = append(items, domain.CoverageItem{Key: fmt.Sprintf("episode:%d", g.ID*100+e), GroupID: g.ID,
					Label: "S E", Wanted: true, HasFile: e%3 != 0, AiredAt: &aired})
			}
			if err := tx.SaveCoverageGroup("sonarr", g.ID, items, now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	cfg, _, _ := config.Parse([]byte("version = 1\n"), "/x/h.toml")
	svc := New(st, cfg, func() time.Time { return now }, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cv, err := svc.Coverage(ctx, "", 0, 100)
		if err != nil || cv.Managers[0].Wanted != 1500 || cv.Managers[0].GapsTotal != 500 {
			b.Fatalf("%v %+v", err, cv.Managers)
		}
	}
}
