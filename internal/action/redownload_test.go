package action

import (
	"errors"
	"strings"
	"testing"
)

// The recovery relies on the manager searching for the replacement itself
// (skipRedownload=false). Released Sonarr/Radarr do not search when
// "Redownload Failed" is off, nor for a release grabbed through interactive
// search unless "Redownload Failed from Interactive Search" is on, so the
// manager step is refused before anything is sent in those cases.
func TestManagerMustOwnTheReplacementSearch(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name string
		rd   *Redownload
		err  string
		ok   bool
	}{
		{"enabled, automatic search grab", &Redownload{Failed: true, FromInteractive: &off, GrabSource: "Search"}, "", true},
		{"enabled, interactive grab allowed", &Redownload{Failed: true, FromInteractive: &on, GrabSource: "InteractiveSearch"}, "", true},
		{"enabled, unknown source, interactive allowed", &Redownload{Failed: true, FromInteractive: &on}, "", true},
		{"redownload off", &Redownload{Failed: false, FromInteractive: &on, GrabSource: "Search"}, "", false},
		{"interactive grab, interactive off", &Redownload{Failed: true, FromInteractive: &off, GrabSource: "InteractiveSearch"}, "", false},
		{"interactive grab, setting not reported", &Redownload{Failed: true, GrabSource: "InteractiveSearch"}, "", false},
		{"unknown source, interactive off", &Redownload{Failed: true, FromInteractive: &off}, "", false},
		{"settings unreadable", nil, "timeout", false},
		{"settings not read", nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := proposal("qbit|aa")
			live := matching(p, false)
			live.Redownload = tc.rd
			if tc.err != "" {
				live.RedownloadErr = errors.New(tc.err).Error()
			}
			var got *Check
			for _, c := range Revalidate(p, 0, live) {
				if c.Name == "manager searches for the replacement" {
					c := c
					got = &c
				}
			}
			if got == nil || got.Satisfied != tc.ok || !got.Hard {
				t.Fatalf("check %+v, want satisfied=%v", got, tc.ok)
			}
			if !tc.ok && got.Detail == "" {
				t.Fatal("a refusal must explain itself")
			}
			if tc.rd != nil && !tc.rd.Failed && !strings.Contains(got.Detail, "Redownload Failed") {
				t.Fatalf("detail %q does not name the setting", got.Detail)
			}
		})
	}
	// After the grab is failed, the client step does not re-check it.
	p := proposal("qbit|aa")
	live := matching(p, true)
	live.Redownload = nil
	for _, c := range Revalidate(p, 1, live) {
		if c.Name == "manager searches for the replacement" {
			t.Fatalf("checked again before the client step: %+v", c)
		}
	}
}
