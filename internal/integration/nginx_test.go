//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/web"
)

// TestNginxProxy puts a real nginx (scripts/it/nginx.sh) in front of the
// documented shared-host setup: Housekeeparr on loopback with a password,
// trusted_proxy, base_path and allowed_hosts, behind the exact location
// blocks from packaging/README.md on a nonstandard HTTPS port. The data is
// synthetic (demo); the web options are built from parsed configuration the
// way `run` builds them.
func TestNginxProxy(t *testing.T) {
	nginx := env(t, "HK_IT_NGINX")
	dir := t.TempDir()
	certFile, keyFile, pool := selfSigned(t, dir, "hk.test")
	const password = "correct horse battery staple"
	t.Setenv("HK_IT_NGINX_WEB_PW", password)
	upstream := freePort(t)

	cfg, _, err := config.Parse([]byte(fmt.Sprintf(`version = 1
[[qbittorrent]]
id = "unused"
url = "http://127.0.0.1:9"
[web]
enabled = true
listen = "127.0.0.1:%d"
password = "env:HK_IT_NGINX_WEB_PW"
trusted_proxy = true
base_path = "/housekeeparr"
allowed_hosts = ["hk.test"]
`, upstream)), filepath.Join(dir, "housekeeparr.toml"))
	if err != nil {
		t.Fatalf("documented shared-host configuration rejected: %v", err)
	}
	secrets, _ := cfg.ResolvedSecrets(nil)
	if secrets["web.password"] != password {
		t.Fatal("web.password did not resolve")
	}
	d, err := demo.Generate(context.Background(), filepath.Join(dir, "state"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Store.Close() })
	d.Config.Web = cfg.Web
	svc := app.New(d.Store, d.Config, d.Clock.Now, d.Engine)
	srv, err := web.New(svc, web.OptionsFromConfig(cfg.Web, secrets["web.password"], nil))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ListenAndServe() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	readme, err := os.ReadFile("../../packaging/README.md")
	if err != nil {
		t.Fatal(err)
	}
	mounted := nginxBlock(t, string(readme), "location /housekeeparr/ {")
	root := nginxBlock(t, string(readme), "location / {")
	if !strings.Contains(mounted, "proxy_set_header Host $http_host;") {
		t.Fatalf("documented block does not pass $http_host:\n%s", mounted)
	}
	// The documented blocks name the default listen port.
	at := func(block string) string {
		return strings.ReplaceAll(block, "127.0.0.1:8484", fmt.Sprintf("127.0.0.1:%d", upstream))
	}

	t.Run("documented shared-host block", func(t *testing.T) {
		port := startNginx(t, nginx, certFile, keyFile, at(mounted))
		b := newBrowser(t, pool, port)
		waitPath(b, "/housekeeparr/healthz")
		origin := b.base

		// Anonymous: a page redirects to sign-in with the nested return
		// path; the read-only API wants credentials.
		r := b.do("GET", "/housekeeparr/attention?x=1", nil, nil)
		if loc := r.Header.Get("Location"); r.StatusCode != http.StatusSeeOther || loc != "/housekeeparr/login?next=%2Fattention%3Fx%3D1" {
			t.Fatalf("anonymous page: %d %q", r.StatusCode, loc)
		}
		securityHeadersOf(t, "sign-in redirect", r)
		if r := b.do("GET", "/housekeeparr/api/v1/overview", nil, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous API: %d", r.StatusCode)
		}
		// Sign in and return to the nested page, prefixed exactly once.
		b.do("GET", "/housekeeparr/login?next="+url.QueryEscape("/attention?x=1"), nil, nil)
		csrf := csrfOf(t, b.lastBody)
		r = b.do("POST", "/housekeeparr/login", url.Values{"csrf": {csrf}, "username": {"admin"}, "password": {password}, "next": {"/attention?x=1"}}, nil)
		if loc := r.Header.Get("Location"); r.StatusCode != http.StatusSeeOther || loc != "/housekeeparr/attention?x=1" {
			t.Fatalf("sign-in through nginx: %d %q %s", r.StatusCode, loc, b.lastBody)
		}
		var session *http.Cookie
		for _, c := range b.lastSetCookies {
			if c.Name == "hk_session" {
				session = c
			}
		}
		if session == nil || session.Path != "/housekeeparr/" || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
			t.Fatalf("session cookie %+v: want Path=/housekeeparr/, Secure, HttpOnly, SameSite=Strict", session)
		}
		t.Logf("session cookie: Path=%s Secure=%v HttpOnly=%v SameSite=Strict", session.Path, session.Secure, session.HttpOnly)
		if r := b.do("GET", "/housekeeparr/attention?x=1", nil, nil); r.StatusCode != http.StatusOK {
			t.Fatalf("signed-in nested page: %d", r.StatusCode)
		}

		// An authorized POST: acknowledge an open finding.
		fs, err := svc.Findings(context.Background(), app.FindingQuery{})
		if err != nil {
			t.Fatal(err)
		}
		var id int64
		for _, f := range fs {
			if f.State == domain.FindingOpen {
				id = f.ID
				break
			}
		}
		if id == 0 {
			t.Fatal("demo data has no open finding")
		}
		fpath := "/housekeeparr/findings/" + strconv.FormatInt(id, 10)
		b.do("GET", fpath, nil, nil)
		r = b.do("POST", fpath+"/ack", url.Values{"csrf": {csrfOf(t, b.lastBody)}}, map[string]string{"Origin": origin})
		if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), fpath) {
			t.Fatalf("acknowledge through nginx: %d %q %s", r.StatusCode, r.Header.Get("Location"), b.lastBody)
		}
		if f, err := svc.Finding(context.Background(), id); err != nil || f.State != domain.FindingAcknowledged {
			t.Fatalf("finding after acknowledgement: %v %v", f.State, err)
		}

		// A foreign Host reaching Housekeeparr through nginx is refused.
		if r := foreignHost(t, b, "/housekeeparr/"); r.StatusCode != http.StatusMisdirectedRequest {
			t.Fatalf("foreign Host: %d", r.StatusCode)
		} else {
			securityHeadersOf(t, "foreign Host", r)
		}

		// With only the mounted location, nginx itself answers the bare
		// mount and paths outside it; Housekeeparr never sees them.
		r = b.do("GET", "/housekeeparr", nil, nil)
		t.Logf("bare mount answered by nginx: %d Location=%q CSP=%q", r.StatusCode, r.Header.Get("Location"), r.Header.Get("Content-Security-Policy"))
		if r.StatusCode != http.StatusMovedPermanently || !strings.HasSuffix(r.Header.Get("Location"), "/housekeeparr/") {
			t.Fatalf("bare mount: %d %q", r.StatusCode, r.Header.Get("Location"))
		}
		r = b.do("GET", "/elsewhere", nil, nil)
		t.Logf("outside the mount answered by nginx: %d CSP=%q", r.StatusCode, r.Header.Get("Content-Security-Policy"))
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("outside the mount: %d", r.StatusCode)
		}
	})

	t.Run("documented root block in front of the mount", func(t *testing.T) {
		// Forwarding everything shows Housekeeparr's own handling of the
		// bare mount and of paths outside it through a real nginx.
		port := startNginx(t, nginx, certFile, keyFile, at(root))
		b := newBrowser(t, pool, port)
		waitPath(b, "/housekeeparr/healthz")
		r := b.do("GET", "/housekeeparr", nil, nil)
		if r.StatusCode != http.StatusMovedPermanently || r.Header.Get("Location") != "/housekeeparr/" {
			t.Fatalf("bare mount: %d %q", r.StatusCode, r.Header.Get("Location"))
		}
		securityHeadersOf(t, "bare mount", r)
		r = b.do("GET", "/elsewhere", nil, nil)
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("outside the mount: %d", r.StatusCode)
		}
		securityHeadersOf(t, "outside the mount", r)
		for _, p := range []string{"/housekeeparr", "/elsewhere"} {
			if r := foreignHost(t, b, p); r.StatusCode != http.StatusMisdirectedRequest {
				t.Fatalf("foreign Host on %s: %d", p, r.StatusCode)
			} else {
				securityHeadersOf(t, "foreign Host on "+p, r)
			}
		}
	})

	t.Run("port-less Host from nginx host variable", func(t *testing.T) {
		port := startNginx(t, nginx, certFile, keyFile, strings.ReplaceAll(at(mounted), "$http_host", "$host"))
		b := newBrowser(t, pool, port)
		waitPath(b, "/housekeeparr/healthz")
		b.do("GET", "/housekeeparr/login", nil, nil)
		r := b.do("POST", "/housekeeparr/login", url.Values{"csrf": {csrfOf(t, b.lastBody)}, "username": {"admin"}, "password": {password}}, nil)
		if r.StatusCode != http.StatusForbidden || !strings.Contains(b.lastBody, "$http_host") {
			t.Fatalf("sign-in with $host: %d %q, want a refusal naming $http_host", r.StatusCode, b.lastBody)
		}
		t.Logf("$host refused as documented: %s", strings.TrimSpace(b.lastBody))
	})
}

// nginxBlock returns the fenced nginx example in the README that starts
// with the given line.
func nginxBlock(t *testing.T, readme, start string) string {
	t.Helper()
	for _, m := range regexp.MustCompile("(?s)```nginx\n(.*?)```").FindAllStringSubmatch(readme, -1) {
		if strings.HasPrefix(m[1], start) {
			return m[1]
		}
	}
	t.Fatalf("packaging/README.md has no nginx example starting %q", start)
	return ""
}

// startNginx runs nginx as the current user with a temporary prefix,
// serving TLS on a free loopback port with the given location block.
func startNginx(t *testing.T, bin, cert, key, location string) int {
	t.Helper()
	prefix := t.TempDir()
	if err := os.MkdirAll(filepath.Join(prefix, "html"), 0o700); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	conf := fmt.Sprintf(`daemon off;
worker_processes 1;
pid %[1]s/nginx.pid;
error_log %[1]s/error.log info;
events { worker_connections 64; }
http {
    access_log %[1]s/access.log;
    client_body_temp_path %[1]s/client_body;
    proxy_temp_path %[1]s/proxy;
    fastcgi_temp_path %[1]s/fastcgi;
    uwsgi_temp_path %[1]s/uwsgi;
    scgi_temp_path %[1]s/scgi;
    server {
        listen 127.0.0.1:%[2]d ssl;
        server_name hk.test;
        ssl_certificate %[3]s;
        ssl_certificate_key %[4]s;
        root %[1]s/html;
%[5]s
    }
}
`, prefix, port, cert, key, location)
	confPath := filepath.Join(prefix, "nginx.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(prefix, "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-p", prefix, "-c", confPath, "-e", filepath.Join(prefix, "error.log"))
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		out.Close()
		if t.Failed() {
			for _, f := range []string{"stdout.log", "error.log", "access.log"} {
				b, _ := os.ReadFile(filepath.Join(prefix, f))
				t.Logf("nginx %s:\n%s", f, lastLines(string(b), 30))
			}
		}
	})
	return port
}

func waitPath(b *browser, path string) {
	b.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		resp, err := b.plain.Get(b.base + path)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("%s not up through nginx: %v %v", path, err, resp)
		}
	}
}

func foreignHost(t *testing.T, b *browser, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", b.base+path, nil)
	u, _ := url.Parse(b.base)
	req.Host = "evil.test:" + u.Port()
	resp, err := b.plain.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func securityHeadersOf(t *testing.T, what string, r *http.Response) {
	t.Helper()
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Cross-Origin-Opener-Policy"} {
		if r.Header.Get(h) == "" {
			t.Errorf("%s: no %s header", what, h)
		}
	}
}

func csrfOf(t *testing.T, body string) string {
	t.Helper()
	m := csrfRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no form token in %q", lastLines(body, 5))
	}
	return m[1]
}
