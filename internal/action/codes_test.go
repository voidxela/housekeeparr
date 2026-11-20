package action

import (
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/store"
)

// Budget and lock checks carry stable codes and, when they clear by
// themselves, when.
func TestCheckCodesAndExpiry(t *testing.T) {
	next := t0.Add(2 * time.Hour)
	full := store.BudgetUsage{Used: 3, Limit: 3, NextAt: &next}
	cases := map[string]struct {
		c     Check
		code  CheckCode
		until *time.Time
	}{
		"pacing":              {BudgetCheck(pacingClaim(3), full), CodePacing, &next},
		"windowed attempts":   {BudgetCheck(BudgetClaim{Kind: store.BudgetAttempts, Scope: "attempts:m|episode:1", Window: time.Hour, Limit: 3, Units: 1}, full), CodeAttempts, &next},
		"reset-only attempts": {BudgetCheck(BudgetClaim{Kind: store.BudgetAttempts, Scope: "attempts:m|episode:1", Limit: 3, Units: 1}, store.BudgetUsage{Used: 3, Limit: 3}), CodeAttempts, nil},
	}
	later := t0.Add(5 * time.Hour)
	locks := lockChecks([]string{"transfer:a", "content:b"}, map[string]int64{"content:b": 7},
		map[string]store.CooldownRecord{"transfer:a": {Until: t0.Add(time.Hour)}, "content:b": {Until: later}}, t0, "")
	cases["held"] = struct {
		c     Check
		code  CheckCode
		until *time.Time
	}{locks[0], CodeLock, nil}
	cases["cooldown: latest end"] = struct {
		c     Check
		code  CheckCode
		until *time.Time
	}{locks[1], CodeCooldown, &later}
	for name, tc := range cases {
		if tc.c.Satisfied || tc.c.Code != tc.code || (tc.c.Until == nil) != (tc.until == nil) || (tc.until != nil && !tc.c.Until.Equal(*tc.until)) {
			t.Errorf("%s: %+v, want code %s until %v", name, tc.c, tc.code, tc.until)
		}
	}
	if ok := BudgetCheck(pacingClaim(5), store.BudgetUsage{Used: 1, Limit: 5}); !ok.Satisfied || ok.Code != "" {
		t.Errorf("a satisfied check has no code: %+v", ok)
	}
}
