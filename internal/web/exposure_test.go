package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/domain"
)

func demoService(t *testing.T, mutate func(*config.Config)) *app.Service {
	t.Helper()
	d, err := demo.Generate(context.Background(), t.TempDir(), time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Store.Close() })
	cfg := *d.Config
	if mutate != nil {
		mutate(&cfg)
	}
	return app.New(d.Store, &cfg, d.Clock.Now, d.Engine)
}

// A declared reverse proxy exposes even a loopback listener: the server
// refuses to start without credentials and host names, exactly as
// configuration validation does. A plain local listener, base_path (which
// is routing only) and loopback TLS need neither.
func TestDeclaredProxyRequiresCredentialsEvenOnLoopback(t *testing.T) {
	svc := demoService(t, nil)
	auth := &Auth{Username: "admin", Password: "p"}
	for name, o := range map[string]Options{
		"proxy without password":   {Listen: "127.0.0.1:8484", AllowedHosts: []string{"box.example"}, TrustedProxy: true, BasePath: "/housekeeparr"},
		"proxy without host names": {Listen: "127.0.0.1:8484", Auth: auth, TrustedProxy: true},
	} {
		if _, err := New(svc, o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, o := range map[string]Options{
		"local only":           {Listen: "127.0.0.1:8484"},
		"local under a path":   {Listen: "127.0.0.1:8484", BasePath: "/housekeeparr"},
		"local TLS":            {Listen: "[::1]:8484", TLSCert: "/c", TLSKey: "/k"},
		"proxy with all three": {Listen: "127.0.0.1:8484", Auth: auth, AllowedHosts: []string{"box.example"}, TrustedProxy: true},
	} {
		if _, err := New(svc, o); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Configuration validation applies the same rule.
	base := "version = 1\n[[qbittorrent]]\nid = \"q\"\nurl = \"http://127.0.0.1:8080\"\n[web]\nenabled = true\nlisten = \"127.0.0.1:8484\"\n"
	for name, extra := range map[string]string{
		"proxy without password":   "trusted_proxy = true\nallowed_hosts = [\"box.example\"]\nbase_path = \"/housekeeparr\"\n",
		"proxy without host names": "trusted_proxy = true\npassword = \"env:HK_PW\"\n",
	} {
		if _, _, err := config.Parse([]byte(base+extra), "/x/housekeeparr.toml"); err == nil {
			t.Errorf("config %s: accepted", name)
		}
	}
	for name, extra := range map[string]string{
		"local only":         "",
		"local under a path": "base_path = \"/housekeeparr\"\n",
		"proxy with all":     "trusted_proxy = true\npassword = \"env:HK_PW\"\nallowed_hosts = [\"box.example\"]\n",
	} {
		if _, _, err := config.Parse([]byte(base+extra), "/x/housekeeparr.toml"); err != nil {
			t.Errorf("config %s: %v", name, err)
		}
	}
}

// proxied serves the documented shared-host setup: loopback listener,
// base_path, trusted_proxy, a password, behind a proxy that passes the
// browser's Host header with its port (nginx: proxy_set_header Host
// $http_host) on https://box.example:8443/housekeeparr/.
type proxied struct {
	h    http.Handler
	host string // Host header the proxy passes
}

func proxiedEnv(t *testing.T, mutate func(*config.Config)) (*proxied, *app.Service) {
	t.Helper()
	svc := demoService(t, mutate)
	s, err := New(svc, Options{Listen: "127.0.0.1:8484", Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Auth: &Auth{Username: "admin", Password: "correct horse"}, AllowedHosts: []string{"box.example"}, TrustedProxy: true, BasePath: "/housekeeparr"})
	if err != nil {
		t.Fatal(err)
	}
	return &proxied{h: s.Handler(), host: "box.example:8443"}, svc
}

func (p *proxied) do(t *testing.T, method, path string, form url.Values, c *http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://box.example:8443")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = p.host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	p.h.ServeHTTP(w, r)
	return w
}

func securityHeaders(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Cross-Origin-Opener-Policy"} {
		if w.Header().Get(h) == "" {
			t.Errorf("%s: no %s header", what, h)
		}
	}
}

func TestMountedInterfaceBehindProxyOnNonstandardPort(t *testing.T) {
	p, svc := proxiedEnv(t, nil)
	// Host and origin checks and headers come first: a foreign Host is
	// refused on the bare mount and outside it, before any redirect or 404.
	for _, path := range []string{"/housekeeparr", "/elsewhere", "/housekeeparr/attention"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Host = "evil.example"
		w := httptest.NewRecorder()
		p.h.ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s with a foreign Host: %d", path, w.Code)
		}
		securityHeaders(t, path+" (foreign host)", w)
	}
	if w := p.do(t, "GET", "/housekeeparr", nil, nil, nil); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/housekeeparr/" {
		t.Fatalf("bare mount: %d %q", w.Code, w.Header().Get("Location"))
	} else {
		securityHeaders(t, "bare mount", w)
	}
	if w := p.do(t, "GET", "/elsewhere", nil, nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("outside the mount: %d", w.Code)
	} else {
		securityHeaders(t, "outside the mount", w)
	}
	// Nested login return: the original page, prefixed exactly once,
	// whether next names it with or without the mount.
	w := p.do(t, "GET", "/housekeeparr/attention?x=1", nil, nil, nil)
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || loc != "/housekeeparr/login?next=%2Fattention%3Fx%3D1" {
		t.Fatalf("unauthenticated page: %d %q", w.Code, loc)
	}
	var cookie *http.Cookie
	for _, next := range []string{"/attention?x=1", "/housekeeparr/attention?x=1"} {
		lp := p.do(t, "GET", "/housekeeparr/login?next="+url.QueryEscape(next), nil, nil, nil)
		w = p.do(t, "POST", "/housekeeparr/login", url.Values{"csrf": {token(t, lp.Body.String())}, "username": {"admin"},
			"password": {"correct horse"}, "next": {next}}, nil, nil)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/housekeeparr/attention?x=1" {
			t.Fatalf("sign-in with next %q: %d %q", next, w.Code, w.Header().Get("Location"))
		}
		cookie = sessionOf(t, w)
		if cookie.Path != "/housekeeparr/" || !cookie.Secure {
			t.Fatalf("session cookie path %q secure %v", cookie.Path, cookie.Secure)
		}
	}
	// An authorized POST through the proxy: acknowledge a finding.
	fs, _ := svc.Findings(context.Background(), app.FindingQuery{})
	var id int64
	for _, f := range fs {
		if f.State == domain.FindingOpen {
			id = f.ID
			break
		}
	}
	page := p.do(t, "GET", "/housekeeparr/findings/"+itoa(id), nil, cookie, nil)
	ack := p.do(t, "POST", "/housekeeparr/findings/"+itoa(id)+"/ack", url.Values{"csrf": {token(t, page.Body.String())}}, cookie, nil)
	if ack.Code != http.StatusSeeOther || !strings.HasPrefix(ack.Header().Get("Location"), "/housekeeparr/findings/") {
		t.Fatalf("authorized POST: %d %q %s", ack.Code, ack.Header().Get("Location"), ack.Body.String())
	}
	if d, _ := svc.Finding(context.Background(), id); d.State != domain.FindingAcknowledged {
		t.Fatalf("finding %s after acknowledgement", d.State)
	}
	// A proxy that drops the port (nginx $host) is told how to fix it.
	p.host = "box.example"
	w = p.do(t, "POST", "/housekeeparr/findings/"+itoa(id)+"/reopen", url.Values{"csrf": {"x"}}, cookie, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "$http_host") {
		t.Fatalf("port-less Host: %d %s", w.Code, w.Body.String())
	}
	p.host = "box.example:8443"

	// Every internal link, form and asset stays under the mount, on every
	// page and in both directions of the Downloads toggle, and every link
	// resolves.
	attr := regexp.MustCompile(`(?:href|action|src)="(/[^"]*)"`)
	seen := map[string]bool{}
	pages := []string{"/housekeeparr/", "/housekeeparr/attention", "/housekeeparr/downloads", "/housekeeparr/downloads?all=1",
		"/housekeeparr/activity", "/housekeeparr/connections", "/housekeeparr/findings/" + itoa(id)}
	for _, pg := range pages {
		w := p.do(t, "GET", pg, nil, cookie, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", pg, w.Code)
		}
		for _, m := range attr.FindAllStringSubmatch(w.Body.String(), -1) {
			link := strings.ReplaceAll(m[1], "&amp;", "&")
			if !strings.HasPrefix(link, "/housekeeparr/") {
				t.Errorf("%s links outside the mount: %q", pg, link)
			}
			seen[link] = true
		}
	}
	for _, want := range []string{"/housekeeparr/downloads?all=1", "/housekeeparr/downloads"} {
		if !seen[want] {
			t.Errorf("Downloads toggle %q not offered", want)
		}
	}
	for link := range seen {
		if strings.Contains(link, "/logout") || regexp.MustCompile(`/(ack|reopen|act|snooze|pause|resume|reset)$`).MatchString(link) {
			continue // form targets (POST)
		}
		if w := p.do(t, "GET", link, nil, cookie, nil); w.Code == http.StatusNotFound {
			t.Errorf("link %q is broken", link)
		}
	}
}

// A crafted link cannot make the interface display arbitrary text.
func TestNoticesAreNotReflected(t *testing.T) {
	e := setup(t)
	body := e.get(t, "/?notice="+url.QueryEscape("Your session expired; sign in at evil.example")).Body.String()
	if strings.Contains(body, "evil.example") || strings.Contains(body, "data-notice") {
		t.Fatal("reflected notice text")
	}
}

// The layout, overview, findings and CLI share one vocabulary for the mode
// as configured now.
func TestModeIsReportedFromCurrentConfiguration(t *testing.T) {
	cases := map[string]struct {
		mutate     func(*config.Config)
		label, has string
	}{
		"observe":   {func(c *config.Config) { c.Mode = config.ModeObserve }, "Observe-only", "No changes are made"},
		"automatic": {func(c *config.Config) { c.Mode = config.ModeAutomatic }, "Automatic", "Manual actions run after a confirmed preview"},
		"paused":    {func(c *config.Config) { c.Mode = config.ModePaused }, "Paused", "No new automation starts"},
		"emergency stop": {func(c *config.Config) { c.Mode = config.ModeAutomatic; c.Automation.EmergencyStop = true },
			"Emergency stop", "Nothing is sent"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc := demoService(t, tc.mutate)
			s, err := New(svc, Options{Listen: "127.0.0.1:0", Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil))})
			if err != nil {
				t.Fatal(err)
			}
			e := &env{h: s.Handler(), svc: svc}
			overview := e.get(t, "/").Body.String()
			if !strings.Contains(overview, `<div class="tile-value">`+tc.label+`</div>`) || !strings.Contains(overview, tc.has) ||
				!strings.Contains(overview, `>`+tc.label+`</span>`) {
				t.Fatalf("overview does not say %q", tc.label)
			}
			if name != "observe" && strings.Contains(overview, "Observe-only") {
				t.Fatal("observe-only claimed")
			}
			f := firstFinding(t, e, domain.FindingTransferStalled)
			page := e.get(t, "/findings/"+itoa(f.ID)).Body.String()
			if !strings.Contains(page, `<span class="pill">`+tc.label+`</span>`) || strings.Contains(page, "not implemented") {
				t.Fatalf("finding page mode for %s", name)
			}
		})
	}
}
