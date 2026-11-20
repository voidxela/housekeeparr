package app

import "testing"

// A multi-episode recovery can be replaced by one download per episode;
// the recovery counts once, with a combined result.
func TestCombineReplacementOutcomes(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{OutcomeImported}, OutcomeImported},
		{[]string{OutcomeImported, OutcomeImported}, OutcomeImported},
		{[]string{OutcomeImported, OutcomeFailed}, OutcomePartial},
		{[]string{OutcomeFailed, OutcomeFailed}, OutcomeFailed},
		{[]string{OutcomeImported, OutcomePending}, OutcomePending},
		{[]string{OutcomeFailed, OutcomeUnknown}, OutcomeUnknown},
	} {
		if got := combineOutcomes(c.in); got != c.want {
			t.Errorf("%v: %s, want %s", c.in, got, c.want)
		}
	}
}
