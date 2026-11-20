package action

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/policy"
)

// Line is one application's API-contract line: the releases that share the
// interface and semantics a write workflow depends on. A version belongs to
// a line when its leading components equal Prefix and it is not older than
// Min (when set). Upstream patch and minor releases within a line are
// supported without code changes; a new major line needs released-instance
// evidence (or the operator's explicit opt-in) before Housekeeparr writes
// through it (SPEC §11, version support).
type Line struct {
	Kind   adapter.Kind
	Prefix string
	Min    string
	// Note says what bounds the line.
	Note string
}

// Lines lists the API-contract lines Housekeeparr knows. A workflow writes
// through a line only when Tested holds at least one passing run for a
// version in that line (for both the manager and the client).
var Lines = []Line{
	{Kind: adapter.KindSonarr, Prefix: "4", Note: "Sonarr v4, API v3"},
	{Kind: adapter.KindRadarr, Prefix: "6", Note: "Radarr v6, API v3"},
	{Kind: adapter.KindQBittorrent, Prefix: "5", Note: "qBittorrent 5, WebUI API v2"},
	{Kind: adapter.KindQBittorrent, Prefix: "4", Min: "4.1", Note: "qBittorrent 4.1 and later, WebUI API v2"},
	{Kind: adapter.KindTransmission, Prefix: "4", Note: "Transmission 4, RPC 17 and later"},
	{Kind: adapter.KindDeluge, Prefix: "2", Note: "Deluge 2"},
	{Kind: adapter.KindRTorrent, Prefix: "0.9", Min: "0.9.8", Note: "rTorrent 0.9.8 and later 0.9 releases"},
}

// releaseOf strips a leading "v" and a trailing build annotation such as
// Transmission's "(38c164933e)".
func releaseOf(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, " ("); i >= 0 {
		v = v[:i]
	}
	return v
}

// compareVersions compares dotted numeric versions; a missing component
// counts as zero and a non-numeric suffix within a component is ignored.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		x, y := 0, 0
		if i < len(as) {
			x = leadingInt(as[i])
		}
		if i < len(bs) {
			y = leadingInt(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func leadingInt(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	v, _ := strconv.Atoi(s[:n])
	return v
}

// LineOf returns the line a version belongs to, if any.
func LineOf(kind adapter.Kind, version string) (Line, bool) {
	r := releaseOf(version)
	if r == "" {
		return Line{}, false
	}
	for _, l := range Lines {
		if l.Kind != kind {
			continue
		}
		if r != l.Prefix && !strings.HasPrefix(r, l.Prefix+".") {
			continue
		}
		if l.Min != "" && compareVersions(r, l.Min) < 0 {
			continue
		}
		return l, true
	}
	return Line{}, false
}

// Side is one application in a version-support decision. A workflow that
// involves no client passes a zero client Side.
type Side struct {
	Kind    adapter.Kind
	Version string
}

func (s Side) zero() bool { return s.Kind == "" && s.Version == "" }

func (s Side) String() string {
	return nonEmpty(string(s.Kind), "an unknown application") + " " + nonEmpty(s.Version, "(version unknown)")
}

// Support is the outcome of a version-support decision.
type Support struct {
	OK bool
	// Level is "tested" (this exact combination passed), "line" (another
	// release of the same lines passed), "opt-in" (the operator allowed
	// versions outside the known lines), or "" when refused.
	Level  string
	Detail string
}

// VersionSupport decides whether workflow wf may write through manager m
// and client c (zero for manager-only workflows). untested is the
// operator's opt-in for versions outside every line with evidence; it still
// requires the workflow to have been tested for the same applications and
// both versions to be known.
func VersionSupport(tested []TestedWorkflow, wf policy.Workflow, m, c Side, untested bool) Support {
	combo := m.String()
	if !c.zero() {
		combo += " with " + c.String()
	}
	var kindPair []string
	sameLines := ""
	for _, tw := range tested {
		if tw.Workflow != wf || tw.Manager != m.Kind || tw.Client != c.Kind {
			continue
		}
		pair := tw.ManagerVersion
		if !c.zero() {
			pair += " with " + string(tw.Client) + " " + tw.ClientVersion
		}
		kindPair = append(kindPair, pair)
		if releaseOf(tw.ManagerVersion) == releaseOf(m.Version) && m.Version != "" &&
			(c.zero() || (c.Version != "" && normVersion(tw.ClientVersion) == normVersion(c.Version))) {
			return Support{OK: true, Level: "tested", Detail: string(wf) + " passed released-instance workflow tests for " + combo}
		}
		if sameLines == "" && inSameLine(m.Kind, tw.ManagerVersion, m.Version) && (c.zero() || inSameLine(c.Kind, tw.ClientVersion, c.Version)) {
			sameLines = string(m.Kind) + " " + pair
		}
	}
	if sameLines != "" {
		ml, _ := LineOf(m.Kind, m.Version)
		lines := ml.Note
		if !c.zero() {
			cl, _ := LineOf(c.Kind, c.Version)
			lines += " and " + cl.Note
		}
		return Support{OK: true, Level: "line", Detail: fmt.Sprintf("%s is in the %s lines, where %s passed released-instance workflow tests (%s); "+
			"each step is still verified live and an unexpected result holds the workflow for review", combo, lines, wf, sameLines)}
	}
	known := m.Version != "" && (c.zero() || c.Version != "")
	if untested && known && len(kindPair) > 0 {
		return Support{OK: true, Level: "opt-in", Detail: fmt.Sprintf("%s is outside the API lines with tested evidence for %s (tested: %s); "+
			"allowed by [automation] untested_versions = \"act\"; each step is still verified live", combo, wf, strings.Join(kindPair, "; "))}
	}
	if len(kindPair) == 0 {
		return Support{Detail: fmt.Sprintf("%s has never passed released-instance tests with %s; this build performs no writes for it (see docs/compatibility.md)", wf, kindNames(m, c))}
	}
	why := "is outside the API lines where " + string(wf) + " passed released-instance tests (" + strings.Join(kindPair, "; ") + ")"
	if !known {
		why = "has an unknown version, so its API line cannot be established"
	}
	return Support{Detail: fmt.Sprintf("%s %s; nothing is sent. Set [automation] untested_versions = \"act\" to allow writes "+
		"to versions outside the tested lines (each step is still verified live)", combo, why)}
}

func inSameLine(kind adapter.Kind, a, b string) bool {
	la, ok1 := LineOf(kind, a)
	lb, ok2 := LineOf(kind, b)
	return ok1 && ok2 && la.Prefix == lb.Prefix && la.Min == lb.Min
}

func kindNames(m, c Side) string {
	s := nonEmpty(string(m.Kind), "an unknown manager")
	if !c.zero() {
		s += " and " + nonEmpty(string(c.Kind), "an unknown client")
	}
	return s
}
