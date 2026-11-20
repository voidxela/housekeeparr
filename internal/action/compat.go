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
// through it (SPEC §4.1, version support).
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

// version is an upstream version string, parsed once. Recorded formats
// (docs/compatibility.md): Sonarr "4.0.20.3014", Radarr "6.4.4.10685",
// qBittorrent "v5.3.0", Transmission "4.0.6 (38c164933e)" (a build hash),
// Deluge "2.2.0", rTorrent "0.9.8".
type version struct {
	// release is the version without a "v"/"V" prefix or build annotation;
	// build is the annotation (Transmission's commit hash), if any.
	release, build string
	nums           []int
	// pre is a prerelease qualifier ("alpha1", "rc1"); "" for a stable
	// release.
	pre string
	// known: something non-blank was reported. valid: it is dot-separated
	// decimal components with an optional prerelease suffix.
	known, valid bool
}

func parseVersion(raw string) version {
	v := version{release: strings.TrimSpace(raw)}
	if v.release == "" {
		return v
	}
	v.known = true
	if r := v.release; r[0] == 'v' || r[0] == 'V' {
		v.release = r[1:]
	}
	if i := strings.IndexAny(v.release, " (+"); i >= 0 {
		v.build = strings.Trim(strings.TrimSpace(v.release[i:]), "()+ ")
		v.release = strings.TrimSpace(v.release[:i])
	}
	core := v.release
	if i := strings.IndexByte(core, '-'); i >= 0 {
		core, v.pre = core[:i], core[i+1:]
		if v.pre == "" {
			return v
		}
	}
	parts := strings.Split(core, ".")
	for i, p := range parts {
		n := 0
		for n < len(p) && p[n] >= '0' && p[n] <= '9' {
			n++
		}
		if n == 0 {
			return v
		}
		if n < len(p) {
			// "4.1.0beta1": a qualifier on the last component only.
			if i != len(parts)-1 || v.pre != "" {
				return v
			}
			v.pre = p[n:]
		}
		x, err := strconv.Atoi(p[:n])
		if err != nil {
			return v
		}
		v.nums = append(v.nums, x)
	}
	v.valid = true
	return v
}

// key identifies an exact build: the release and its build annotation.
func (v version) key() string { return v.release + "|" + v.build }

// compare orders versions numerically (missing components are zero); a
// prerelease precedes its release.
func (v version) compare(o version) int {
	for i := 0; i < len(v.nums) || i < len(o.nums); i++ {
		x, y := 0, 0
		if i < len(v.nums) {
			x = v.nums[i]
		}
		if i < len(o.nums) {
			y = o.nums[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.pre != "" && o.pre == "":
		return -1
	case v.pre == "" && o.pre != "":
		return 1
	}
	return 0
}

// hasPrefix reports whether v's leading components equal p's.
func (v version) hasPrefix(p version) bool {
	if len(v.nums) < len(p.nums) {
		return false
	}
	for i, n := range p.nums {
		if v.nums[i] != n {
			return false
		}
	}
	return true
}

// releaseOf strips a leading "v" and a trailing build annotation such as
// Transmission's "(38c164933e)".
func releaseOf(v string) string { return parseVersion(v).release }

// LineOf returns the line a version belongs to, if any. Only well-formed
// stable releases belong to a line: a malformed version, or a prerelease
// (even of a release inside the line), does not.
func LineOf(kind adapter.Kind, raw string) (Line, bool) {
	v := parseVersion(raw)
	if !v.valid || v.pre != "" {
		return Line{}, false
	}
	for _, l := range Lines {
		if l.Kind != kind || !v.hasPrefix(parseVersion(l.Prefix)) {
			continue
		}
		if l.Min != "" && v.compare(parseVersion(l.Min)) < 0 {
			continue
		}
		return l, true
	}
	return Line{}, false
}

// SupportedLine returns the line a version belongs to when that line has
// released-instance evidence: some registered passing run (Tested) used a
// version in it, as manager or client. A registered line with no evidence
// is not supported.
func SupportedLine(kind adapter.Kind, raw string) (Line, bool) {
	l, ok := LineOf(kind, raw)
	if !ok {
		return Line{}, false
	}
	for _, tw := range Tested {
		if (tw.Manager == kind && inSameLine(kind, tw.ManagerVersion, raw)) || (tw.Client == kind && inSameLine(kind, tw.ClientVersion, raw)) {
			return l, true
		}
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
	pm, pc := parseVersion(m.Version), parseVersion(c.Version)
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
		// An exact registered build is tested whatever its format.
		if pm.known && parseVersion(tw.ManagerVersion).key() == pm.key() &&
			(c.zero() || (pc.known && parseVersion(tw.ClientVersion).key() == pc.key())) {
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
	known := pm.known && (c.zero() || pc.known)
	valid := pm.valid && (c.zero() || pc.valid)
	if untested && valid && len(kindPair) > 0 {
		return Support{OK: true, Level: "opt-in", Detail: fmt.Sprintf("%s is outside the API lines with tested evidence for %s (tested: %s); "+
			"allowed by [automation] untested_versions = \"act\"; each step is still verified live", combo, wf, strings.Join(kindPair, "; "))}
	}
	if len(kindPair) == 0 {
		return Support{Detail: fmt.Sprintf("%s has never passed released-instance tests with %s; this build performs no writes for it (see docs/compatibility.md)", wf, kindNames(m, c))}
	}
	switch {
	case !known:
		// The opt-in requires a known version, so it is not offered.
		return Support{Detail: combo + " has an unknown version, so its API line cannot be established; nothing is sent. " +
			"Check that the application reports its version (see Connections)"}
	case !valid:
		return Support{Detail: fmt.Sprintf("%s reports a version Housekeeparr cannot parse as a release, so its API line cannot be established; "+
			"nothing is sent, even with untested_versions = \"act\"", combo)}
	}
	why := "is outside the API lines where " + string(wf) + " passed released-instance tests (" + strings.Join(kindPair, "; ") + ")"
	if pm.pre != "" || pc.pre != "" {
		why = "is a prerelease, which no supported line includes (tested: " + strings.Join(kindPair, "; ") + ")"
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
