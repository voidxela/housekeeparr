package action

import (
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/policy"
)

func TestVersionSupportFollowsAPILines(t *testing.T) {
	son := func(v string) Side { return Side{adapter.KindSonarr, v} }
	tr := func(v string) Side { return Side{adapter.KindTransmission, v} }
	qb := func(v string) Side { return Side{adapter.KindQBittorrent, v} }
	rad := func(v string) Side { return Side{adapter.KindRadarr, v} }
	np := policy.WorkflowNoProgressRecovery
	cases := []struct {
		name     string
		wf       policy.Workflow
		m, c     Side
		untested bool
		level    string
	}{
		{"exact tested pair", np, son("4.0.20.3014"), tr("4.0.6 (38c164933e)"), false, "tested"},
		{"another build of a tested release", np, son("4.0.20.3014"), tr("4.0.6 (deadbeef00)"), false, "line"},
		{"older patch in the same lines", np, son("4.0.20.3014"), tr("4.0.4 (24adbb24dd)"), false, "line"},
		{"newer manager patch", np, son("4.0.15.2941"), qb("5.1.2"), false, "line"},
		{"leading v", np, son("v4.0.20.3014"), qb("v5.0.0"), false, "line"},
		{"qBittorrent 4 line above its floor", np, son("4.0.20.3014"), qb("4.3.9"), false, "line"},
		{"qBittorrent below the WebUI v2 floor", np, son("4.0.20.3014"), qb("4.0.4"), false, ""},
		{"new manager major", np, son("5.0.0.1"), qb("5.3.0"), false, ""},
		{"new manager major, opted in", np, son("5.0.0.1"), qb("5.3.0"), true, "opt-in"},
		{"older Radarr major without evidence", np, rad("5.26.2.10099"), qb("5.3.0"), false, ""},
		{"Transmission 3, opted in", np, son("4.0.20.3014"), tr("3.00 (bb6b5a062e)"), true, "opt-in"},
		{"unknown client version even when opted in", np, son("4.0.20.3014"), tr(""), true, ""},
		{"partial pack is Sonarr only", policy.WorkflowPartialPack, rad("6.4.4.10685"), qb("5.3.0"), true, ""},
		{"manager-only search, same line", policy.WorkflowMissingSearch, son("4.0.17.2952"), Side{}, false, "line"},
		{"manager-only search, other major", policy.WorkflowMissingSearch, rad("5.26.2.10099"), Side{}, false, ""},
	}
	for _, c := range cases {
		s := VersionSupport(Tested, c.wf, c.m, c.c, c.untested)
		if s.OK != (c.level != "") || s.Level != c.level {
			t.Errorf("%s: got ok=%v level=%q (%s), want level %q", c.name, s.OK, s.Level, s.Detail, c.level)
		}
		if !s.OK && !strings.Contains(s.Detail, "nothing is sent") && !strings.Contains(s.Detail, "no writes") {
			t.Errorf("%s: refusal does not say nothing is sent: %s", c.name, s.Detail)
		}
	}
}

func TestLineOf(t *testing.T) {
	for v, want := range map[string]string{"0.9.8": "0.9", "0.9.10": "0.9", "0.9.7": "", "0.10.0": "", "4.1.4 (a6d907d9ea)": "4", "": ""} {
		kind := adapter.KindTransmission
		if strings.HasPrefix(v, "0.") {
			kind = adapter.KindRTorrent
		}
		l, ok := LineOf(kind, v)
		if got := map[bool]string{true: l.Prefix}[ok]; got != want {
			t.Errorf("LineOf(%s, %q) = %q, want %q", kind, v, got, want)
		}
	}
}

// Every version string recorded in docs/compatibility.md parses as a
// stable release, and malformed, blank and prerelease input is refused
// the way the operator can act on.
func TestVersionParsingAndRefusals(t *testing.T) {
	for _, tw := range Tested {
		for _, v := range []string{tw.ManagerVersion, tw.ClientVersion} {
			if v == "" {
				continue
			}
			p := parseVersion(v)
			if !p.valid || p.pre != "" || p.release == "" {
				t.Errorf("recorded %q: %+v", v, p)
			}
		}
	}
	if p := parseVersion("4.0.6 (38c164933e)"); p.release != "4.0.6" || p.build != "38c164933e" {
		t.Errorf("build annotation: %+v", p)
	}
	son := func(v string) Side { return Side{adapter.KindSonarr, v} }
	qb := func(v string) Side { return Side{adapter.KindQBittorrent, v} }
	rt := func(v string) Side { return Side{adapter.KindRTorrent, v} }
	np := policy.WorkflowNoProgressRecovery
	cases := []struct {
		name     string
		m, c     Side
		untested bool
		level    string
		hint     bool // the refusal suggests untested_versions = "act"
	}{
		{"uppercase V prefix", son("V4.0.20.3014"), qb("V5.3.0"), false, "tested", false},
		{"surrounding whitespace", son(" 4.0.20.3014 "), qb("5.3.0\n"), false, "tested", false},
		{"stable patch upgrade", son("4.0.21.3100"), qb("5.3.2"), false, "line", false},
		{"blank client version", son("4.0.20.3014"), qb("   "), true, "", false},
		{"blank manager version", son(""), qb("5.3.0"), false, "", false},
		{"malformed component", son("4.abc"), qb("5.3.0"), true, "", false},
		{"trailing dot", son("4."), qb("5.3.0"), true, "", false},
		{"empty prerelease", son("4.0.20.3014"), qb("5.3.0-"), true, "", false},
		{"prerelease at the qBittorrent floor", son("4.0.20.3014"), qb("4.1.0-alpha1"), false, "", true},
		{"prerelease at the qBittorrent floor, opted in", son("4.0.20.3014"), qb("4.1.0-alpha1"), true, "opt-in", false},
		{"prerelease inside the line", son("4.0.20.3014"), qb("5.2.0beta1"), false, "", true},
		{"rTorrent prerelease at the floor", son("4.0.20.3014"), rt("0.9.8-rc1"), false, "", true},
		{"rTorrent below the floor", son("4.0.20.3014"), rt("0.9.7"), false, "", true},
	}
	for _, c := range cases {
		s := VersionSupport(Tested, np, c.m, c.c, c.untested)
		if s.OK != (c.level != "") || s.Level != c.level {
			t.Errorf("%s: ok=%v level=%q (%s), want level %q", c.name, s.OK, s.Level, s.Detail, c.level)
		}
		if !s.OK && strings.Contains(s.Detail, "untested_versions = \"act\" to allow") != c.hint {
			t.Errorf("%s: opt-in hint %v, want %v: %s", c.name, !c.hint, c.hint, s.Detail)
		}
	}
	// A registered line with no released-instance evidence is not
	// reported as supported.
	saved := Lines
	defer func() { Lines = saved }()
	Lines = append(append([]Line(nil), saved...), Line{Kind: adapter.KindRadarr, Prefix: "7", Note: "Radarr v7 (hypothetical)"})
	if _, ok := LineOf(adapter.KindRadarr, "7.0.1.1"); !ok {
		t.Fatal("the registered line is not found")
	}
	if l, ok := SupportedLine(adapter.KindRadarr, "7.0.1.1"); ok {
		t.Fatalf("a line without evidence reported supported: %+v", l)
	}
	if _, ok := SupportedLine(adapter.KindRadarr, "6.5.0.1"); !ok {
		t.Fatal("a line with evidence not supported")
	}
}
