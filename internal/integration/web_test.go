//go:build integration

package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestWebDeployment exercises the production binary's web interface as a
// remote deployment, with a cookie-keeping, Origin- and Sec-Fetch-sending
// client standing in for a browser, in both documented topologies:
//
//   - direct TLS: `listen` on a non-loopback address with tls_cert/tls_key;
//   - trusted proxy: a TLS-terminating reverse proxy in front of a plain
//     listener with trusted_proxy = true. The proxy must pass the original
//     Host header (nginx: `proxy_set_header Host $host`); a proxy that
//     rewrites it to the upstream address is shown to be refused, with an
//     explanation.
//
// It listens on 0.0.0.0, so it runs only inside the harness's loopback-only
// network namespace. The site is reached as https://hk.test (resolved to
// loopback by the client).
func TestWebDeployment(t *testing.T) {
	env(t, "HK_IT_IN_NETNS")
	qbtURL := env(t, "HK_IT_QBT_URL")
	dir := t.TempDir()
	bin := filepath.Join(dir, "housekeeparr")
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "../../cmd/housekeeparr")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	certFile, keyFile, pool := selfSigned(t, dir, "hk.test")
	const password = "correct horse battery staple"
	pwFile := filepath.Join(dir, "web-password")
	if err := os.WriteFile(pwFile, []byte(password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, name, webBlock string) (stop func()) {
		t.Helper()
		state := filepath.Join(dir, name)
		cfg := filepath.Join(dir, name+".toml")
		src := fmt.Sprintf("version = 1\n[state]\ndir = %q\n[polling]\njitter = \"0s\"\n[web]\nenabled = true\npassword = \"file:%s\"\nallowed_hosts = [\"hk.test\"]\nsession_ttl = \"20s\"\n%s\n"+
			"[[qbittorrent]]\nid = \"qbit\"\nurl = %q\nusername = \"admin\"\npassword = \"env:HK_IT_QBT_PASS\"\n", state, pwFile, webBlock, qbtURL)
		if err := os.WriteFile(cfg, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		logf, err := os.Create(filepath.Join(dir, name+".log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "run", "--config", cfg)
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
			logf.Close()
			if t.Failed() {
				b, _ := os.ReadFile(logf.Name())
				t.Logf("%s log:\n%s", name, lastLines(string(b), 30))
			}
		}
	}

	t.Run("direct TLS", func(t *testing.T) {
		port := freePort(t)
		stop := run(t, "tls", fmt.Sprintf("listen = \"0.0.0.0:%d\"\ntls_cert = %q\ntls_key = %q", port, certFile, keyFile))
		defer func() { stop() }()
		b := newBrowser(t, pool, port)
		b.waitUp()
		exerciseSessions(t, b, password, func() {
			stop()
			stop = run(t, "tls", fmt.Sprintf("listen = \"0.0.0.0:%d\"\ntls_cert = %q\ntls_key = %q", port, certFile, keyFile))
		})
		if r := b.do("GET", "/healthz", nil, nil); r.Header.Get("Strict-Transport-Security") == "" {
			t.Error("no HSTS header over direct TLS")
		}
		// Plain HTTP on the TLS port gets no page.
		if resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port)); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Errorf("plain HTTP answered on the TLS listener: %s", body)
			}
		}
	})

	t.Run("trusted proxy", func(t *testing.T) {
		upstream := freePort(t)
		stop := run(t, "proxy", fmt.Sprintf("listen = \"0.0.0.0:%d\"\ntrusted_proxy = true", upstream))
		defer func() { stop() }()
		target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", upstream))
		for _, tc := range []struct {
			name        string
			rewriteHost bool
		}{{"host preserved", false}, {"host rewritten to the upstream", true}} {
			t.Run(tc.name, func(t *testing.T) {
				rp := httputil.NewSingleHostReverseProxy(target) // keeps the client's Host
				if tc.rewriteHost {
					inner := rp.Director
					rp.Director = func(r *http.Request) { inner(r); r.Host = target.Host } // nginx's default
				}
				front := freePort(t)
				ln, err := tls.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", front), &tls.Config{Certificates: []tls.Certificate{loadPair(t, certFile, keyFile)}})
				if err != nil {
					t.Fatal(err)
				}
				srv := &http.Server{Handler: rp}
				go func() { _ = srv.Serve(ln) }()
				defer srv.Close()
				b := newBrowser(t, pool, front)
				b.waitUp()
				if !tc.rewriteHost {
					exerciseSessions(t, b, password, func() {
						stop()
						stop = run(t, "proxy", fmt.Sprintf("listen = \"0.0.0.0:%d\"\ntrusted_proxy = true", upstream))
					})
					return
				}
				csrf := b.loginToken()
				r := b.do("POST", "/login", url.Values{"csrf": {csrf}, "username": {"admin"}, "password": {password}}, nil)
				body := b.lastBody
				if r.StatusCode != http.StatusForbidden || !strings.Contains(body, "Host") {
					t.Fatalf("sign-in through a Host-rewriting proxy: %d %q, want a refusal that names the Host header", r.StatusCode, body)
				}
				t.Logf("Host-rewriting proxy refused as documented: %s", strings.TrimSpace(body))
			})
		}
	})
}

// exerciseSessions signs in, checks the session cookie's attributes,
// mutation protections, the read-only API, restart invalidation, sign-out,
// session expiry and the sign-in limit.
func exerciseSessions(t *testing.T, b *browser, password string, restart func()) {
	t.Helper()
	if r := b.do("GET", "/", nil, nil); r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(r.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous page: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := b.do("GET", "/api/v1/overview", nil, nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous API: %d", r.StatusCode)
	}
	b.signIn("admin", password)
	var session *http.Cookie
	for _, c := range b.lastSetCookies {
		if c.Name == "hk_session" {
			session = c
		}
	}
	if session == nil || !session.Secure || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookie %+v: want Secure, HttpOnly, SameSite=Strict", session)
	}
	if r := b.do("GET", "/", nil, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("signed-in page: %d", r.StatusCode)
	}
	token := b.formToken()
	// A cross-site form post (a browser marks it) and a post without the
	// token are refused; the session survives both.
	if r := b.do("POST", "/logout", url.Values{"csrf": {token}}, map[string]string{"Origin": "https://evil.test", "Sec-Fetch-Site": "cross-site"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site post: %d", r.StatusCode)
	}
	if r := b.do("POST", "/logout", url.Values{}, nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("post without a token: %d", r.StatusCode)
	}
	if r := b.do("GET", "/", nil, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("session lost after refused posts: %d", r.StatusCode)
	}
	// The read-only API with HTTP Basic, without a session.
	req, _ := http.NewRequest("GET", b.base+"/api/v1/overview", nil)
	req.SetBasicAuth("admin", password)
	if resp, err := b.plain.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("API with Basic: %v %v", err, resp)
	} else {
		resp.Body.Close()
	}
	// Sessions end when Housekeeparr restarts (per-process signing key).
	restart()
	b.waitUp()
	if r := b.do("GET", "/", nil, nil); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("session survived a restart: %d", r.StatusCode)
	}
	// Sign out with the session's token.
	b.signIn("admin", password)
	if r := b.do("GET", "/", nil, nil); r.StatusCode != http.StatusOK {
		t.Fatalf("second sign-in: %d", r.StatusCode)
	}
	if r := b.do("POST", "/logout", url.Values{"csrf": {b.formToken()}}, nil); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("sign-out: %d %s", r.StatusCode, b.lastBody)
	}
	if r := b.do("GET", "/", nil, nil); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("signed out but still served: %d", r.StatusCode)
	}
	// Expiry (session_ttl 20s).
	b.signIn("admin", password)
	time.Sleep(21 * time.Second)
	if r := b.do("GET", "/", nil, nil); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("session outlived session_ttl: %d", r.StatusCode)
	}
	// Five failures pause sign-in, also for the right password.
	for i := 0; i < 5; i++ {
		if r := b.do("POST", "/login", url.Values{"csrf": {b.loginToken()}, "username": {"admin"}, "password": {"wrong"}}, nil); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d", i, r.StatusCode)
		}
	}
	if r := b.do("POST", "/login", url.Values{"csrf": {b.loginToken()}, "username": {"admin"}, "password": {password}}, nil); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("sign-in after five failures: %d", r.StatusCode)
	}
}

type browser struct {
	t              *testing.T
	base           string
	c, plain       *http.Client
	lastBody       string
	lastSetCookies []*http.Cookie
}

func newBrowser(t *testing.T, pool *x509.CertPool, port int) *browser {
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "hk.test"},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
		}}
	noFollow := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &browser{t: t, base: fmt.Sprintf("https://hk.test:%d", port),
		c:     &http.Client{Jar: jar, Transport: tr, CheckRedirect: noFollow, Timeout: 30 * time.Second},
		plain: &http.Client{Transport: tr, CheckRedirect: noFollow, Timeout: 30 * time.Second}}
}

func (b *browser) waitUp() {
	b.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		resp, err := b.plain.Get(b.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
		if time.Now().After(deadline) {
			b.t.Fatalf("web interface not up: %v", err)
		}
	}
}

// do sends what a browser sends for a same-origin navigation or form post.
func (b *browser) do(method, path string, form url.Values, headers map[string]string) *http.Response {
	b.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, b.base+path, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if method == "POST" {
		req.Header.Set("Origin", b.base)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	b.lastBody, b.lastSetCookies = string(raw), resp.Cookies()
	return resp
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

func (b *browser) loginToken() string {
	b.t.Helper()
	b.do("GET", "/login", nil, nil)
	m := csrfRE.FindStringSubmatch(b.lastBody)
	if m == nil {
		b.t.Fatalf("no sign-in token: %s", b.lastBody)
	}
	return m[1]
}

func (b *browser) formToken() string {
	b.t.Helper()
	b.do("GET", "/", nil, nil)
	m := csrfRE.FindStringSubmatch(b.lastBody)
	if m == nil {
		b.t.Fatalf("no form token on the overview")
	}
	return m[1]
}

func (b *browser) signIn(user, pass string) {
	b.t.Helper()
	r := b.do("POST", "/login", url.Values{"csrf": {b.loginToken()}, "username": {user}, "password": {pass}, "next": {"/"}}, nil)
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/" {
		b.t.Fatalf("sign-in: %d %s %q", r.StatusCode, r.Header.Get("Location"), b.lastBody)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func selfSigned(t *testing.T, dir, host string) (string, string, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(c)
	return cert, keyPath, pool
}

func loadPair(t *testing.T, cert, key string) tls.Certificate {
	t.Helper()
	p, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
