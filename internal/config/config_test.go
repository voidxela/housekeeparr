package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/policy"
)

const minimal = `
version = 1

[[sonarr]]
id = "sonarr"
url = "http://127.0.0.1:8989"
api_key = "env:SONARR_API_KEY"
download_clients = { "qBittorrent" = "qbit" }

[[qbittorrent]]
id = "qbit"
url = "http://qbittorrent:8080"
username = "admin"
password = "file:/run/secrets/qbit"
`

func mustParse(t *testing.T, src string) *Config {
	t.Helper()
	cfg, _, err := Parse([]byte(src), "/etc/housekeeparr/housekeeparr.toml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return cfg
}

func expectProblem(t *testing.T, src, field, contains string) {
	t.Helper()
	_, probs, err := Parse([]byte(src), "/x/housekeeparr.toml")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected validation error for %s, got %v", field, err)
	}
	for _, p := range probs {
		if p.Severity == "error" && strings.HasPrefix(p.Field, field) && strings.Contains(p.Message, contains) {
			return
		}
	}
	t.Fatalf("no error for field %q containing %q; got %v", field, contains, probs)
}

func TestMinimalConfigDefaults(t *testing.T) {
	cfg := mustParse(t, minimal)
	if cfg.Mode != ModeObserve {
		t.Errorf("default mode = %s, want observe", cfg.Mode)
	}
	if cfg.Web.Enabled {
		t.Error("web must be disabled by default")
	}
	if cfg.State.Dir != "/etc/housekeeparr/state" {
		t.Errorf("relative state dir not resolved against config file: %s", cfg.State.Dir)
	}
	if cfg.Sonarr[0].QueuePageSize != 200 || cfg.Sonarr[0].MaxQueuePages != 25 {
		t.Errorf("pagination defaults not applied: %+v", cfg.Sonarr[0])
	}
	if len(cfg.Revision) != 12 {
		t.Errorf("revision missing")
	}
}

func TestRejectsLiteralSecrets(t *testing.T) {
	expectProblem(t, strings.Replace(minimal, `"env:SONARR_API_KEY"`, `"0123456789abcdef"`, 1), "sonarr.sonarr.api_key", "references")
	expectProblem(t, strings.Replace(minimal, `"file:/run/secrets/qbit"`, `"file:relative/path"`, 1), "qbittorrent.qbit.password", "absolute")
}

func TestRejectsUnknownKeysAndPlannedSections(t *testing.T) {
	expectProblem(t, minimal+"\n[polling]\nintervall = \"5m\"\n", "polling.intervall", "unknown")
	expectProblem(t, minimal+"\n[[lidarr]]\nid = \"l\"\n", "lidarr", "unknown")
}

func TestModeValidation(t *testing.T) {
	cfg := mustParse(t, strings.Replace(minimal, "version = 1", "version = 1\nmode = \"automatic\"\n[automation]\nemergency_stop = true", 1))
	if cfg.Mode != ModeAutomatic {
		t.Errorf("mode = %q, want automatic", cfg.Mode)
	}
	if !cfg.Automation.EmergencyStop {
		t.Error("expected emergency_stop = true")
	}
	cfgPaused := mustParse(t, strings.Replace(minimal, "version = 1", "version = 1\nmode = \"paused\"", 1))
	if cfgPaused.Mode != ModePaused {
		t.Errorf("mode = %q, want paused", cfgPaused.Mode)
	}
	expectProblem(t, strings.Replace(minimal, "version = 1", "version = 1\nmode = \"turbo\"", 1), "mode", "must be")
}

func TestRequiresVersion(t *testing.T) {
	expectProblem(t, strings.Replace(minimal, "version = 1", "", 1), "version", "required")
	expectProblem(t, strings.Replace(minimal, "version = 1", "version = 2", 1), "version", "unsupported")
}

func TestWebMustBindLoopback(t *testing.T) {
	expectProblem(t, minimal+"\n[web]\nenabled = true\nlisten = \"0.0.0.0:8484\"\n", "web.listen", "loopback")
	expectProblem(t, minimal+"\n[web]\nenabled = true\nlisten = \"192.168.1.5:8484\"\n", "web.listen", "loopback")
	cfg := mustParse(t, minimal+"\n[web]\nenabled = true\nlisten = \"[::1]:8484\"\n")
	if !cfg.Web.Enabled {
		t.Fatal("web not enabled")
	}
}

func TestPlainHTTPToRemoteHostNeedsAcknowledgement(t *testing.T) {
	remote := strings.Replace(minimal, "http://qbittorrent:8080", "http://seedbox.example.com:8080", 1)
	expectProblem(t, remote, "qbittorrent.qbit.url", "plain HTTP")
	acked := strings.Replace(remote, `username = "admin"`, "username = \"admin\"\nallow_insecure_http = true", 1)
	_, probs, err := Parse([]byte(acked), "/x/h.toml")
	if err != nil {
		t.Fatalf("acknowledged insecure http should be allowed: %v", err)
	}
	found := false
	for _, p := range probs {
		found = found || (p.Severity == "warning" && strings.Contains(p.Message, "allow_insecure_http"))
	}
	if !found {
		t.Fatal("expected a warning for acknowledged plain HTTP")
	}
	for _, host := range []string{"10.0.0.2", "192.168.1.10", "sonarr", "nas.lan", "box.home.arpa", "localhost"} {
		if !isLocalNetworkHost(host) {
			t.Errorf("%s should be treated as local", host)
		}
	}
}

func TestDuplicateIDsAndUnknownMapping(t *testing.T) {
	dup := minimal + "\n[[qbittorrent]]\nid = \"qbit\"\nurl = \"http://127.0.0.1:8081\"\n"
	expectProblem(t, dup, "qbittorrent[1].id", "duplicate")
	bad := strings.Replace(minimal, `"qBittorrent" = "qbit"`, `"qBittorrent" = "nope"`, 1)
	expectProblem(t, bad, "sonarr.sonarr.download_clients", "unknown client")
}

func TestEffectivePolicyPrecedenceAndSources(t *testing.T) {
	src := minimal + `
[polling]
interval = "5m"

[policy]
no_progress_window = "2h"
`
	src = strings.Replace(src, `password = "file:/run/secrets/qbit"`, "password = \"file:/run/secrets/qbit\"\n[qbittorrent.policy]\nno_progress_window = \"3h\"\nmin_observations = 5", 1)
	cfg := mustParse(t, src)
	global, err := cfg.EffectivePolicy("")
	if err != nil {
		t.Fatal(err)
	}
	if global.Settings.NoProgressWindow != 2*time.Hour {
		t.Errorf("global window = %s", global.Settings.NoProgressWindow)
	}
	if global.Settings.MaxObservationGap != 10*time.Minute {
		t.Errorf("max gap should follow 2 × interval, got %s", global.Settings.MaxObservationGap)
	}
	client, err := cfg.EffectivePolicy("qbit")
	if err != nil {
		t.Fatal(err)
	}
	if client.Settings.NoProgressWindow != 3*time.Hour || client.Settings.MinObservations != 5 {
		t.Errorf("client overrides not applied: %+v", client.Settings)
	}
	if client.Revision == global.Revision {
		t.Error("different effective policies must have different revisions")
	}
	sources := map[string]string{}
	for _, s := range client.Sources {
		sources[s.Setting] = s.From
	}
	if sources["no_progress_window"] != "config [qbittorrent.qbit.policy]" {
		t.Errorf("source of no_progress_window = %q", sources["no_progress_window"])
	}
	if sources["metadata_timeout"] != "built-in proposed default" {
		t.Errorf("source of metadata_timeout = %q", sources["metadata_timeout"])
	}
}

func TestInvalidPolicyIsRejected(t *testing.T) {
	expectProblem(t, minimal+"\n[policy]\nmin_observations = 1\n", "policy", "min_observations")
	expectProblem(t, minimal+"\n[policy]\nmax_observation_gap = \"1m\"\n", "policy", "max_observation_gap")
}

func TestSecretResolution(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "pw")
	if err := os.WriteFile(good, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(dir, "loose")
	if err := os.WriteFile(loose, []byte("s3cret"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, warns, err := SecretRef("file:" + good).Resolve(nil)
	if err != nil || v != "s3cret" || len(warns) != 0 {
		t.Fatalf("file secret: %q %v %v", v, warns, err)
	}
	_, warns, err = SecretRef("file:" + loose).Resolve(nil)
	if err != nil || len(warns) != 1 {
		t.Fatalf("expected permission warning, got %v %v", warns, err)
	}
	env := func(k string) (string, bool) {
		if k == "SET" {
			return "value", true
		}
		return "", false
	}
	if v, _, err := SecretRef("env:SET").Resolve(env); err != nil || v != "value" {
		t.Fatalf("env secret: %q %v", v, err)
	}
	_, _, err = SecretRef("env:MISSING").Resolve(env)
	if err == nil || strings.Contains(err.Error(), "value") {
		t.Fatalf("missing env must fail without values: %v", err)
	}
	_, _, err = SecretRef("file:" + filepath.Join(dir, "absent")).Resolve(nil)
	if err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	cfg, probs, err := Load(filepath.Join("..", "..", "housekeeparr.example.toml"))
	if err != nil {
		t.Fatalf("example configuration is invalid: %v", err)
	}
	for _, p := range probs {
		t.Logf("example config problem: %s", p)
	}
	if cfg.Mode != ModeObserve || !cfg.Web.Enabled {
		t.Fatalf("unexpected example config: mode=%s web=%v", cfg.Mode, cfg.Web.Enabled)
	}
}

func TestRadarrSection(t *testing.T) {
	cfg := mustParse(t, minimal+"\n[[radarr]]\nid = \"radarr\"\nurl = \"http://127.0.0.1:7878\"\napi_key = \"env:RADARR_KEY\"\ndownload_clients = { \"qBittorrent\" = \"qbit\" }\n")
	if len(cfg.Radarr) != 1 || cfg.Radarr[0].QueuePageSize != 200 {
		t.Fatalf("%+v", cfg.Radarr)
	}
	expectProblem(t, minimal+"\n[[radarr]]\nid = \"radarr\"\nurl = \"http://127.0.0.1:7878\"\n", "radarr.radarr.api_key", "required")
	expectProblem(t, minimal+"\n[[radarr]]\nid = \"sonarr\"\nurl = \"http://127.0.0.1:7878\"\napi_key = \"env:K\"\n", "radarr[0].id", "duplicate")
}

func TestTransmissionSection(t *testing.T) {
	src := minimal + "\n[[transmission]]\nid = \"tr\"\nurl = \"http://127.0.0.1:9091\"\nusername = \"u\"\npassword = \"env:TR_PW\"\n[transmission.policy]\nno_progress_window = \"2h\"\n"
	cfg := mustParse(t, src)
	if len(cfg.Clients()) != 2 {
		t.Fatalf("clients %+v", cfg.Clients())
	}
	eff, err := cfg.EffectivePolicy("tr")
	if err != nil || eff.Settings.NoProgressWindow != 2*time.Hour {
		t.Fatalf("%+v %v", eff.Settings, err)
	}
	for _, s := range eff.Sources {
		if s.Setting == "no_progress_window" && s.From != "config [transmission.tr.policy]" {
			t.Fatalf("source %q", s.From)
		}
	}
	_, probs := cfg.ResolvedSecrets(func(string) (string, bool) { return "", false })
	found := false
	for _, p := range probs {
		found = found || p.Field == "transmission.tr.password"
	}
	if !found {
		t.Fatal("transmission password secret not resolved")
	}
	expectProblem(t, strings.Replace(src, `id = "tr"`, "id = \"tr\"\nrpc_path = \"rpc\"", 1), "transmission.tr.rpc_path", "must start")
	expectProblem(t, strings.Replace(minimal, `username = "admin"`, "username = \"admin\"\nrpc_path = \"/x\"", 1), "qbittorrent.qbit.rpc_path", "only applies to Transmission and rTorrent")
}

func TestDelugeSection(t *testing.T) {
	src := minimal + "\n[[deluge]]\nid = \"dl\"\nurl = \"http://127.0.0.1:8112\"\npassword = \"env:DL_PW\"\n"
	cfg := mustParse(t, src)
	if len(cfg.Clients()) != 2 || cfg.Clients()[1].Section != "deluge" {
		t.Fatalf("%+v", cfg.Clients())
	}
	expectProblem(t, strings.Replace(src, `id = "dl"`, "id = \"dl\"\nusername = \"x\"", 1), "deluge.dl.username", "password only")
	expectProblem(t, strings.Replace(src, `password = "env:DL_PW"`, "", 1), "deluge.dl.password", "required")
}

func TestRTorrentSection(t *testing.T) {
	cfg := mustParse(t, minimal+"\n[[rtorrent]]\nid = \"rt\"\nurl = \"https://seedbox.example.com/rutorrent\"\nrpc_path = \"/RPC2\"\nusername = \"u\"\npassword = \"env:RT\"\n")
	if ce := cfg.Clients(); len(ce) != 2 || ce[1].Section != "rtorrent" || ce[1].Config.RPCPath != "/RPC2" {
		t.Fatalf("%+v", ce)
	}
}

func TestValidationRejectsInvalidAutomationRulesOverridesExclusions(t *testing.T) {
	withSecondClient := minimal + `
[[transmission]]
id = "tr"
url = "http://127.0.0.1:9091"
`
	tests := []struct {
		name     string
		extra    string
		field    string
		contains []string
	}{
		{
			name:     "unknown key in automation",
			extra:    "\n[automation]\nkill_switch = true\n",
			field:    "automation.kill_switch",
			contains: []string{"unknown"},
		},
		{
			name:     "unknown workflow in automation.workflows",
			extra:    "\n[automation.workflows]\nbogus_workflow = true\n",
			field:    "automation.workflows.bogus_workflow",
			contains: []string{"unknown workflow"},
		},
		{
			name:     "unknown key in rule",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\nextra = 1\n[rule.match]\nclient = \"qbit\"\n",
			field:    "rule.extra",
			contains: []string{"unknown"},
		},
		{
			name:     "unknown key in rule.match",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\n[rule.match]\nclient = \"qbit\"\ntag = \"x\"\n",
			field:    "rule.match.tag",
			contains: []string{"unknown"},
		},
		{
			name:     "unknown key in rule.policy",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\n[rule.match]\nclient = \"qbit\"\n[rule.policy]\nbad_setting = \"1m\"\n",
			field:    "rule.policy.bad_setting",
			contains: []string{"unknown"},
		},
		{
			name:     "unknown workflow in rule.automation",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\n[rule.match]\nclient = \"qbit\"\n[rule.automation]\nnope = true\n",
			field:    "rule[0].automation.nope",
			contains: []string{"unknown workflow"},
		},
		{
			name:     "rule missing name",
			extra:    "\n[[rule]]\npriority = 1\n[rule.match]\nclient = \"qbit\"\n",
			field:    "rule[0].name",
			contains: []string{"required"},
		},
		{
			name:     "duplicate rule name",
			extra:    "\n[[rule]]\nname = \"dup\"\npriority = 1\n[rule.match]\nclient = \"qbit\"\n[[rule]]\nname = \"dup\"\npriority = 2\n[rule.match]\nclient = \"tr\"\n",
			field:    "rule[1].name",
			contains: []string{"duplicate"},
		},
		{
			name:     "rule missing priority",
			extra:    "\n[[rule]]\nname = \"r\"\n[rule.match]\nclient = \"qbit\"\n",
			field:    "rule[0].priority",
			contains: []string{"required"},
		},
		{
			name:     "rule empty match",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\n",
			field:    "rule[0].match",
			contains: []string{"at least one match field"},
		},
		{
			name:     "rule category without client",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\n[rule.match]\ncategory = \"tv\"\n",
			field:    "rule[0].match.category",
			contains: []string{"requires client"},
		},
		{
			name:     "rule unknown client id",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\n[rule.match]\nclient = \"ghost\"\n",
			field:    "rule[0].match.client",
			contains: []string{"unknown client"},
		},
		{
			name:     "rule unknown manager id",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\n[rule.match]\nmanager = \"radarr\"\n",
			field:    "rule[0].match.manager",
			contains: []string{"unknown manager"},
		},
		{
			name:     "rule invalid privacy",
			extra:    "\n[[rule]]\nname = \"r\"\npriority = 1\n[rule.match]\nprivacy = \"secret\"\n",
			field:    "rule[0].match.privacy",
			contains: []string{"public"},
		},
		{
			name: "equal priority conflict on setting names both rules",
			extra: `
[[rule]]
name = "alpha"
priority = 10
[rule.match]
client = "qbit"
[rule.policy]
no_progress_window = "2h"

[[rule]]
name = "beta"
priority = 10
[rule.match]
privacy = "private"
[rule.policy]
no_progress_window = "4h"
`,
			field:    "rule[1]",
			contains: []string{"alpha", "beta", "same priority"},
		},
		{
			name: "equal priority conflict on workflow names both rules",
			extra: `
[[rule]]
name = "enable-qbit"
priority = 5
[rule.match]
client = "qbit"
[rule.automation]
no_progress_recovery = true

[[rule]]
name = "disable-sonarr"
priority = 5
[rule.match]
manager = "sonarr"
[rule.automation]
no_progress_recovery = false
`,
			field:    "rule[1]",
			contains: []string{"enable-qbit", "disable-sonarr", "no_progress_recovery"},
		},
		{
			name:     "override unknown key",
			extra:    "\n[[override]]\ntransfer = \"qbit|abc\"\nreason = \"why\"\nextra = true\n",
			field:    "override.extra",
			contains: []string{"unknown"},
		},
		{
			name:     "override unknown policy key",
			extra:    "\n[[override]]\ntransfer = \"qbit|abc\"\nreason = \"why\"\n[override.policy]\nfoo = \"1m\"\n",
			field:    "override.policy.foo",
			contains: []string{"unknown"},
		},
		{
			name:     "override unknown workflow",
			extra:    "\n[[override]]\ntransfer = \"qbit|abc\"\nreason = \"why\"\n[override.automation]\nfoo = true\n",
			field:    "override[0].automation.foo",
			contains: []string{"unknown workflow"},
		},
		{
			name:     "override missing reason",
			extra:    "\n[[override]]\ntransfer = \"qbit|abc\"\n",
			field:    "override[0].reason",
			contains: []string{"required"},
		},
		{
			name:     "override neither transfer nor content",
			extra:    "\n[[override]]\nreason = \"why\"\n",
			field:    "override[0]",
			contains: []string{"either transfer or content"},
		},
		{
			name:     "override both transfer and content",
			extra:    "\n[[override]]\ntransfer = \"qbit|abc\"\ncontent = \"sonarr|episode:1\"\nreason = \"why\"\n",
			field:    "override[0]",
			contains: []string{"cannot set both"},
		},
		{
			name:     "override unknown client in transfer",
			extra:    "\n[[override]]\ntransfer = \"nope|abc\"\nreason = \"why\"\n",
			field:    "override[0].transfer",
			contains: []string{"unknown client"},
		},
		{
			name:     "override unknown manager in content",
			extra:    "\n[[override]]\ncontent = \"radarr|movie:1\"\nreason = \"why\"\n",
			field:    "override[0].content",
			contains: []string{"unknown manager"},
		},
		{
			name:     "duplicate transfer override",
			extra:    "\n[[override]]\ntransfer = \"qbit|abc\"\nreason = \"one\"\n[[override]]\ntransfer = \"qbit|abc\"\nreason = \"two\"\n",
			field:    "override[1].transfer",
			contains: []string{"duplicate"},
		},
		{
			name:     "legacy release-specific content key",
			extra:    "\n[[override]]\ncontent = \"sonarr|0123abcd|episode:12\"\nreason = \"old\"\n",
			field:    "override[0].content",
			contains: []string{"episode:<id>", "no longer accepted"},
		},
		{
			name:     "legacy exclusion content key",
			extra:    "\n[[exclude]]\ncontent = \"sonarr|0123abcd|episode:12\"\nreason = \"old\"\n",
			field:    "exclude[0].content",
			contains: []string{"no longer accepted"},
		},
		{
			name:     "duplicate content override",
			extra:    "\n[[override]]\ncontent = \"sonarr|episode:12\"\nreason = \"one\"\n[[override]]\ncontent = \"sonarr|episode:12\"\nreason = \"two\"\n",
			field:    "override[1].content",
			contains: []string{"duplicate"},
		},
		{
			name:     "exclude unknown key",
			extra:    "\n[[exclude]]\nclient = \"qbit\"\nreason = \"r\"\nextra = 1\n",
			field:    "exclude.extra",
			contains: []string{"unknown"},
		},
		{
			name:     "exclude category without client",
			extra:    "\n[[exclude]]\ncategory = \"tv\"\nreason = \"r\"\n",
			field:    "exclude[0].category",
			contains: []string{"requires client"},
		},
		{
			name:     "exclude multiple targets",
			extra:    "\n[[exclude]]\nclient = \"qbit\"\nmanager = \"sonarr\"\nreason = \"r\"\n",
			field:    "exclude[0]",
			contains: []string{"only one"},
		},
		{
			name:     "exclude missing target",
			extra:    "\n[[exclude]]\nreason = \"r\"\n",
			field:    "exclude[0]",
			contains: []string{"exactly one"},
		},
		{
			name:     "exclude missing reason",
			extra:    "\n[[exclude]]\nclient = \"qbit\"\n",
			field:    "exclude[0].reason",
			contains: []string{"required"},
		},
		{
			name:     "exclude unknown workflow",
			extra:    "\n[[exclude]]\nclient = \"qbit\"\nworkflows = [\"nope\"]\nreason = \"r\"\n",
			field:    "exclude[0].workflows",
			contains: []string{"unknown workflow"},
		},
		{
			name:     "exclude unknown client",
			extra:    "\n[[exclude]]\nclient = \"ghost\"\nreason = \"r\"\n",
			field:    "exclude[0].client",
			contains: []string{"unknown client"},
		},
		{
			name:     "exclude unknown manager",
			extra:    "\n[[exclude]]\nmanager = \"ghost\"\nreason = \"r\"\n",
			field:    "exclude[0].manager",
			contains: []string{"unknown manager"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, probs, err := Parse([]byte(withSecondClient+tc.extra), "/x/housekeeparr.toml")
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %v", err)
			}
			for _, p := range probs {
				if p.Severity != "error" || !strings.HasPrefix(p.Field, tc.field) {
					continue
				}
				allMatch := true
				for _, sub := range tc.contains {
					if !strings.Contains(p.Message, sub) {
						allMatch = false
						break
					}
				}
				if allMatch {
					return
				}
			}
			t.Fatalf("no error matching field %q containing %v; got %v", tc.field, tc.contains, probs)
		})
	}
}

func TestEqualPriorityDisjointAndCompatibleRulesAllowed(t *testing.T) {
	src := minimal + `
[[transmission]]
id = "tr"
url = "http://127.0.0.1:9091"

[[rule]]
name = "public-fast"
priority = 10
[rule.match]
privacy = "public"
[rule.policy]
no_progress_window = "1h"

[[rule]]
name = "private-slow"
priority = 10
[rule.match]
privacy = "private"
[rule.policy]
no_progress_window = "6h"

[[rule]]
name = "qbit-tv"
priority = 10
[rule.match]
client = "qbit"
category = "tv"
[rule.policy]
min_observations = 4

[[rule]]
name = "qbit-movies"
priority = 10
[rule.match]
client = "qbit"
category = "movies"
privacy = "public"
[rule.policy]
min_observations = 6

[[rule]]
name = "sonarr-same-value"
priority = 10
[rule.match]
manager = "sonarr"
privacy = "private"
[rule.policy]
min_observations = 4
metadata_timeout = "45m"
`
	cfg := mustParse(t, src)
	eff, err := cfg.Resolve(policy.Subject{
		ClientID:  "qbit",
		Category:  "tv",
		ManagerID: "sonarr",
		Privacy:   "private",
	})
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if eff.Settings.NoProgressWindow != 6*time.Hour {
		t.Errorf("NoProgressWindow = %s, want 6h", eff.Settings.NoProgressWindow)
	}
	if eff.Settings.MinObservations != 4 {
		t.Errorf("MinObservations = %d, want 4", eff.Settings.MinObservations)
	}
	if eff.Settings.MetadataTimeout != 45*time.Minute {
		t.Errorf("MetadataTimeout = %s, want 45m", eff.Settings.MetadataTimeout)
	}
}

func TestResolvePrecedenceProvenanceOverridesAndExclusions(t *testing.T) {
	src := strings.Replace(minimal, "version = 1", "version = 1\nmode = \"automatic\"", 1)
	src = strings.Replace(src, `password = "file:/run/secrets/qbit"`, "password = \"file:/run/secrets/qbit\"\n[qbittorrent.policy]\nno_progress_window = \"3h\"\nimport_settle = \"45m\"", 1)
	src += `
[automation]
emergency_stop = false

[automation.workflows]
no_progress_recovery = true

[policy]
no_progress_window = "2h"
metadata_timeout = "90m"

# Intentionally list priority 20 before priority 10 to verify ascending priority order.
[[rule]]
name = "high-prio-private"
priority = 20
[rule.match]
client = "qbit"
privacy = "private"
[rule.policy]
no_progress_window = "6h"
[rule.automation]
no_progress_recovery = false
metadata_recovery = true

[[rule]]
name = "slow-private"
priority = 10
[rule.match]
client = "qbit"
privacy = "private"
[rule.policy]
no_progress_window = "4h"
near_complete_window = "8h"
[rule.automation]
no_progress_recovery = true
partial_pack = true

[[override]]
content = "sonarr|episode:12"
reason = "manual episode override"
[override.policy]
near_complete_window = "10h"

[[override]]
transfer = "qbit|abcd"
reason = "rare release"
[override.policy]
no_progress_window = "12h"
[override.automation]
no_progress_recovery = true

[[exclude]]
manager = "sonarr"
workflows = ["missing_search"]
reason = "no auto searches"

[[exclude]]
client = "qbit"
category = "keep"
workflows = ["no_progress_recovery"]
reason = "curated keep category"
`
	cfg := mustParse(t, src)

	tests := []struct {
		name           string
		sub            policy.Subject
		wantNoProg     time.Duration
		wantNearWin    time.Duration
		wantMeta       time.Duration
		wantImport     time.Duration
		wantSources    map[string]string
		wantEnabled    map[policy.Workflow]bool
		wantAutoFrom   map[policy.Workflow]string
		wantPermits    map[policy.Workflow]bool
		wantBlockerSub map[policy.Workflow]string
	}{
		{
			name:        "global subject inherits [policy] and [automation.workflows]",
			sub:         policy.Subject{},
			wantNoProg:  2 * time.Hour,
			wantNearWin: 4 * time.Hour,
			wantMeta:    90 * time.Minute,
			wantImport:  30 * time.Minute,
			wantSources: map[string]string{
				"no_progress_window":   "config [policy]",
				"metadata_timeout":     "config [policy]",
				"near_complete_window": "built-in proposed default",
			},
			wantEnabled: map[policy.Workflow]bool{
				policy.WorkflowNoProgressRecovery: true,
				policy.WorkflowMetadataRecovery:   false,
			},
			wantAutoFrom: map[policy.Workflow]string{
				policy.WorkflowNoProgressRecovery: "config [automation.workflows]",
				policy.WorkflowMetadataRecovery:   "built-in default",
			},
			wantPermits: map[policy.Workflow]bool{
				policy.WorkflowNoProgressRecovery: true,
				policy.WorkflowMetadataRecovery:   false,
			},
		},
		{
			name:        "higher priority rule wins over lower priority and client policy",
			sub:         policy.Subject{ClientID: "qbit", Privacy: "private"},
			wantNoProg:  6 * time.Hour,
			wantNearWin: 8 * time.Hour,
			wantMeta:    90 * time.Minute,
			wantImport:  45 * time.Minute,
			wantSources: map[string]string{
				"no_progress_window":   `config rule "high-prio-private" (priority 20)`,
				"near_complete_window": `config rule "slow-private" (priority 10)`,
				"import_settle":        "config [qbittorrent.qbit.policy]",
				"metadata_timeout":     "config [policy]",
			},
			wantEnabled: map[policy.Workflow]bool{
				policy.WorkflowNoProgressRecovery: false,
				policy.WorkflowMetadataRecovery:   true,
				policy.WorkflowPartialPack:        true,
			},
			wantAutoFrom: map[policy.Workflow]string{
				policy.WorkflowNoProgressRecovery: `config rule "high-prio-private" (priority 20)`,
				policy.WorkflowMetadataRecovery:   `config rule "high-prio-private" (priority 20)`,
				policy.WorkflowPartialPack:        `config rule "slow-private" (priority 10)`,
			},
			wantPermits: map[policy.Workflow]bool{
				policy.WorkflowNoProgressRecovery: false,
				policy.WorkflowMetadataRecovery:   true,
				policy.WorkflowPartialPack:        true,
			},
		},
		{
			name:        "override applies after rules and exclusion still blocks",
			sub:         policy.Subject{ClientID: "qbit", Category: "keep", NativeID: "abcd", ManagerID: "sonarr", ContentKey: "episode:12", Privacy: "private"},
			wantNoProg:  12 * time.Hour,
			wantNearWin: 10 * time.Hour,
			wantMeta:    90 * time.Minute,
			wantImport:  45 * time.Minute,
			wantSources: map[string]string{
				"no_progress_window":   "config override transfer qbit|abcd",
				"near_complete_window": "config override content sonarr|episode:12",
			},
			wantEnabled: map[policy.Workflow]bool{
				policy.WorkflowNoProgressRecovery: true,
			},
			wantAutoFrom: map[policy.Workflow]string{
				policy.WorkflowNoProgressRecovery: "config override transfer qbit|abcd",
			},
			wantPermits: map[policy.Workflow]bool{
				policy.WorkflowNoProgressRecovery: false, // blocked by exclude #2 despite override enabling it
				policy.WorkflowMissingSearch:      false, // blocked by exclude #1 + not enabled
				policy.WorkflowMetadataRecovery:   true,  // enabled by rule, not excluded
			},
			wantBlockerSub: map[policy.Workflow]string{
				policy.WorkflowNoProgressRecovery: "excluded: curated keep category (config exclude #2)",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eff, err := cfg.Resolve(tc.sub)
			if err != nil {
				t.Fatalf("Resolve error: %v", err)
			}
			if eff.Settings.NoProgressWindow != tc.wantNoProg {
				t.Errorf("NoProgressWindow = %s, want %s", eff.Settings.NoProgressWindow, tc.wantNoProg)
			}
			if eff.Settings.NearCompleteWindow != tc.wantNearWin {
				t.Errorf("NearCompleteWindow = %s, want %s", eff.Settings.NearCompleteWindow, tc.wantNearWin)
			}
			if eff.Settings.MetadataTimeout != tc.wantMeta {
				t.Errorf("MetadataTimeout = %s, want %s", eff.Settings.MetadataTimeout, tc.wantMeta)
			}
			if eff.Settings.ImportSettle != tc.wantImport {
				t.Errorf("ImportSettle = %s, want %s", eff.Settings.ImportSettle, tc.wantImport)
			}
			gotSources := map[string]string{}
			for _, s := range eff.Sources {
				gotSources[s.Setting] = s.From
			}
			for k, wantFrom := range tc.wantSources {
				if gotSources[k] != wantFrom {
					t.Errorf("Source[%s] = %q, want %q", k, gotSources[k], wantFrom)
				}
			}
			gotAuto := map[policy.Workflow]policy.WorkflowPermission{}
			for _, p := range eff.Automation {
				gotAuto[p.Workflow] = p
			}
			for wf, want := range tc.wantEnabled {
				if gotAuto[wf].Enabled != want {
					t.Errorf("Automation[%s].Enabled = %v, want %v", wf, gotAuto[wf].Enabled, want)
				}
			}
			for wf, wantFrom := range tc.wantAutoFrom {
				if gotAuto[wf].From != wantFrom {
					t.Errorf("Automation[%s].From = %q, want %q", wf, gotAuto[wf].From, wantFrom)
				}
			}
			for wf, wantOK := range tc.wantPermits {
				ok, reasons := eff.Permits(wf)
				if ok != wantOK {
					t.Errorf("Permits(%s) = %v (reasons=%v), want %v", wf, ok, reasons, wantOK)
				}
				if sub, has := tc.wantBlockerSub[wf]; has && !slices.Contains(reasons, sub) {
					t.Errorf("Permits(%s) reasons = %v, want %q", wf, reasons, sub)
				}
			}
		})
	}
}

func TestRevisionChangesWithRuleAndStableAcrossLoads(t *testing.T) {
	base := minimal + `
[automation.workflows]
no_progress_recovery = true
metadata_recovery = false
partial_pack = false

[[rule]]
name = "priv"
priority = 10
[rule.match]
client = "qbit"
privacy = "private"
[rule.policy]
no_progress_window = "4h"
[rule.automation]
no_progress_recovery = false
`
	cfg1 := mustParse(t, base)
	cfg2 := mustParse(t, base)

	sub := policy.Subject{ClientID: "qbit", Privacy: "private"}
	eff1, err := cfg1.Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	eff2, err := cfg2.Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if eff1.Revision != eff2.Revision {
		t.Errorf("effective revision not stable across loads: %s != %s", eff1.Revision, eff2.Revision)
	}
	if cfg1.Revision != cfg2.Revision {
		t.Errorf("config revision not stable across loads: %s != %s", cfg1.Revision, cfg2.Revision)
	}

	changed := strings.Replace(base, `no_progress_window = "4h"`, `no_progress_window = "5h"`, 1)
	cfg3 := mustParse(t, changed)
	eff3, err := cfg3.Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if eff3.Revision == eff1.Revision {
		t.Error("effective revision must change when matching rule policy changes")
	}
	if cfg3.Revision == cfg1.Revision {
		t.Error("config revision must change when rule changes")
	}

	changedAuto := strings.Replace(base, "no_progress_recovery = false", "no_progress_recovery = true", 1)
	cfg4 := mustParse(t, changedAuto)
	eff4, err := cfg4.Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if eff4.Revision == eff1.Revision {
		t.Error("effective revision must change when matching rule automation changes")
	}
}
