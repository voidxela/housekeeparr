package store

import (
	"fmt"
	"testing"
	"time"
)

// seedAttempt journals an intent and reserves one attempt unit for scope
// at the given time; released returns it, as an unsent action does.
func seedAttempt(t *testing.T, s *Store, scope string, at time.Time, released bool) {
	t.Helper()
	err := s.Write(ctx, func(tx *Tx) error {
		in := testIntent(fmt.Sprintf("%s|%d|%v", scope, at.UnixNano(), released))
		id, err := tx.CreateIntent(in, at)
		if err != nil {
			return err
		}
		if err := tx.ReserveBudget(id, Budget{Kind: BudgetAttempts, Scope: scope, Limit: 1 << 20}, 1, at); err != nil {
			return err
		}
		if released {
			return tx.ReleaseBudget(id, "refused", at)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func usage(t *testing.T, s *Store, scope string, window time.Duration, now time.Time) int {
	t.Helper()
	u, err := s.BudgetUsage(ctx, Budget{Kind: BudgetAttempts, Scope: scope, Window: window, Limit: 3}, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	return u.Used
}

// Pruning removes exactly the rows that no longer count once they are past
// the audit horizon: released rows, rows aged out of a rolling window, and
// (reset-only) rows at or before the latest reset. Outstanding reset-only
// counts survive pruning and a restart.
func TestAttemptPruningMatchesBudgetBoundaries(t *testing.T) {
	keep := 30 * 24 * time.Hour
	now := t0.Add(100 * 24 * time.Hour)
	old := now.Add(-40 * 24 * time.Hour) // past the horizon
	recent := now.Add(-time.Hour)
	for name, window := range map[string]time.Duration{"rolling window": 24 * time.Hour, "reset-only": 0} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s := openTest(t, dir)
			seedAttempt(t, s, "attempts:m|episode:1", old, false)    // no reset
			seedAttempt(t, s, "attempts:m|episode:1", recent, false) // counts
			seedAttempt(t, s, "attempts:m|episode:2", old, false)    // before its reset
			seedAttempt(t, s, "attempts:m|episode:3", old, true)     // released
			seedAttempt(t, s, "attempts:m|episode:4", recent, true)  // released, recent: kept as history
			if err := s.Write(ctx, func(tx *Tx) error { return tx.ResetBudget("attempts:m|episode:2", "op", "n", old.Add(time.Minute)) }); err != nil {
				t.Fatal(err)
			}
			before := map[string]int{}
			for _, sc := range []string{"attempts:m|episode:1", "attempts:m|episode:2", "attempts:m|episode:3"} {
				before[sc] = usage(t, s, sc, window, now)
			}
			if err := s.Write(ctx, func(tx *Tx) error {
				_, err := tx.Prune(now, Retention{Observations: keep, ConnectionChecks: keep, ResolvedFindings: keep, AuditEvents: keep, ContentAttemptWindow: window})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s = openTest(t, dir)
			for sc, n := range before {
				if got := usage(t, s, sc, window, now); got != n {
					t.Errorf("%s: counted %d before pruning and restart, %d after", sc, n, got)
				}
			}
			var rows int
			if err := s.r.QueryRow("SELECT count(*) FROM action_attempts").Scan(&rows); err != nil {
				t.Fatal(err)
			}
			want := 2 // the recent counted row and the recent released row
			if window == 0 {
				want = 3 // the old row of a never-reset scope is still outstanding
			}
			if rows != want {
				t.Errorf("%d ledger rows remain, want %d", rows, want)
			}
		})
	}
}

// Summaries group scopes before paging, so a counted scope whose rows are
// older than 500 newer rows elsewhere still appears, and every count equals
// what dispatch counts, including at the window's exact boundary.
func TestAttemptCountsAgreeWithDispatch(t *testing.T) {
	s := openTest(t, t.TempDir())
	window := 24 * time.Hour
	now := t0.Add(48 * time.Hour)
	// An old-but-counting scope, then 520 newer rows across 26 scopes.
	seedAttempt(t, s, "attempts:m|episode:900", now.Add(-20*time.Hour), false)
	for i := 0; i < 520; i++ {
		seedAttempt(t, s, fmt.Sprintf("attempts:m|episode:%d", i%26), now.Add(-time.Duration(10*60-i)*time.Second), false)
	}
	// Released units and rows before a reset do not count.
	seedAttempt(t, s, "attempts:m|episode:901", now.Add(-time.Hour), true)
	seedAttempt(t, s, "attempts:m|episode:902", now.Add(-2*time.Hour), false)
	if err := s.Write(ctx, func(tx *Tx) error { return tx.ResetBudget("attempts:m|episode:902", "op", "n", now.Add(-time.Hour)) }); err != nil {
		t.Fatal(err)
	}
	// A row exactly at the window's start does not count.
	seedAttempt(t, s, "attempts:m|episode:903", now.Add(-window), false)
	counts, total, err := s.AttemptCounts(ctx, "attempts:", window, 3, now, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if total != 27 || len(counts) != 27 {
		t.Fatalf("total %d, listed %d, want 27 (26 busy scopes and the old one)", total, len(counts))
	}
	found := false
	for _, c := range counts {
		if c.Scope == "attempts:m|episode:900" {
			found = true
		}
		if want := usage(t, s, c.Scope, window, now); c.Used != want {
			t.Errorf("%s: summary %d, dispatch %d", c.Scope, c.Used, want)
		}
	}
	if !found {
		t.Error("the older scope is missing")
	}
	// Paging covers each scope once.
	seen := map[string]bool{}
	for off := 0; off < total; off += 10 {
		pg, _, err := s.AttemptCounts(ctx, "attempts:", window, 3, now, off, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range pg {
			if seen[c.Scope] {
				t.Fatalf("%s listed twice", c.Scope)
			}
			seen[c.Scope] = true
		}
	}
	if len(seen) != total {
		t.Fatalf("paged %d of %d", len(seen), total)
	}
	// One minute later the old scope's row is still in the window; a day
	// later it has aged out, in both views alike.
	for _, at := range []time.Time{now.Add(time.Minute), now.Add(4*time.Hour + time.Millisecond)} {
		cs, _, _ := s.AttemptCounts(ctx, "attempts:m|episode:900", window, 3, at, 0, 10)
		if n := usage(t, s, "attempts:m|episode:900", window, at); (len(cs) == 1) != (n == 1) {
			t.Errorf("at %s: summary %+v, dispatch %d", at, cs, n)
		}
	}
}
