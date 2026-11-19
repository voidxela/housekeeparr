package config

import (
	"strings"
	"testing"
)

const notifyBase = "version = 1\n"

func TestNotifyTargets(t *testing.T) {
	cfg, probs, err := Parse([]byte(notifyBase+"[[notify]]\nid = \"phone\"\nurl = \"env:HK_NTFY\"\nformat = \"text\"\n"), "/x/h.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		if p.Severity == "error" {
			t.Fatalf("valid target rejected: %v", p)
		}
	}
	n := cfg.Notify[0]
	if n.MinSeverity != "warning" || n.MaxPerHour != 10 || n.Format != "text" {
		t.Fatalf("defaults %+v", n)
	}
	sec, _ := cfg.ResolvedSecrets(func(k string) (string, bool) { return "https://ntfy.example/topic", k == "HK_NTFY" })
	if sec["notify.phone.url"] != "https://ntfy.example/topic" {
		t.Fatalf("secrets %v", sec)
	}
	for name, src := range map[string]string{
		"literal URL":   "[[notify]]\nid = \"a\"\nurl = \"https://hooks.example/token\"\n",
		"missing URL":   "[[notify]]\nid = \"a\"\n",
		"bad format":    "[[notify]]\nid = \"a\"\nurl = \"env:X\"\nformat = \"xml\"\n",
		"bad severity":  "[[notify]]\nid = \"a\"\nurl = \"env:X\"\nmin_severity = \"info\"\n",
		"rate too high": "[[notify]]\nid = \"a\"\nurl = \"env:X\"\nmax_per_hour = 1000\n",
		"duplicate id":  "[[notify]]\nid = \"a\"\nurl = \"env:X\"\n[[notify]]\nid = \"a\"\nurl = \"env:Y\"\n",
		"bad link":      "[[notify]]\nid = \"a\"\nurl = \"env:X\"\nlink_base = \"hk.local\"\n",
	} {
		_, probs, err := Parse([]byte(notifyBase+src), "/x/h.toml")
		bad := err != nil
		for _, p := range probs {
			bad = bad || p.Severity == "error" && strings.HasPrefix(p.Field, "notify")
		}
		if !bad {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestWebRemoteExposureRequirements(t *testing.T) {
	ok := "[web]\nenabled = true\nlisten = \"0.0.0.0:8484\"\npassword = \"env:HK_WEB\"\nallowed_hosts = [\"hk.example.org\"]\ntrusted_proxy = true\n"
	cfg, probs, err := Parse([]byte(notifyBase+ok), "/x/h.toml")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range probs {
		if p.Severity == "error" {
			t.Fatalf("valid remote web rejected: %v", p)
		}
	}
	if cfg.Web.Username != "admin" {
		t.Fatalf("username default %q", cfg.Web.Username)
	}
	for name, src := range map[string]string{
		"no password":      "[web]\nenabled = true\nlisten = \"0.0.0.0:8484\"\nallowed_hosts = [\"h\"]\ntrusted_proxy = true\n",
		"no hosts":         "[web]\nenabled = true\nlisten = \"0.0.0.0:8484\"\npassword = \"env:HK_WEB\"\ntrusted_proxy = true\n",
		"no TLS or proxy":  "[web]\nenabled = true\nlisten = \"0.0.0.0:8484\"\npassword = \"env:HK_WEB\"\nallowed_hosts = [\"h\"]\n",
		"literal password": "[web]\nenabled = true\nlisten = \"127.0.0.1:8484\"\npassword = \"hunter2\"\n",
		"half TLS":         "[web]\nenabled = true\nlisten = \"127.0.0.1:8484\"\ntls_cert = \"/c.pem\"\n",
		"relative TLS":     "[web]\nenabled = true\nlisten = \"127.0.0.1:8484\"\ntls_cert = \"c.pem\"\ntls_key = \"k.pem\"\n",
	} {
		_, probs, _ := Parse([]byte(notifyBase+src), "/x/h.toml")
		bad := false
		for _, p := range probs {
			bad = bad || p.Severity == "error" && strings.HasPrefix(p.Field, "web")
		}
		if !bad {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestNotificationDeliveryBoundsAreConfigurable(t *testing.T) {
	const raw = `
version = 1
[notifications]
digest_threshold = 2
max_attempts = 3
max_age = "3h"
initial_backoff = "2m"
max_backoff = "10m"
request_timeout = "5s"
`
	if _, _, err := Parse([]byte(raw), ""); err != nil {
		t.Fatal(err)
	}
}

func TestWebSessionAndSignInBoundsAreConfigurable(t *testing.T) {
	const raw = `
version = 1
[web]
session_ttl = "2h"
sign_in_window = "5m"
sign_in_failures = 3
`
	if _, _, err := Parse([]byte(raw), ""); err != nil {
		t.Fatal(err)
	}
}
