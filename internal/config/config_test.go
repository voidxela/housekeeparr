package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	expectProblem(t, minimal+"\n[[transmission]]\nid = \"tr\"\n", "transmission", "not implemented")
}

func TestRejectsAutomaticMode(t *testing.T) {
	expectProblem(t, strings.Replace(minimal, "version = 1", "version = 1\nmode = \"automatic\"", 1), "mode", "not available")
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
