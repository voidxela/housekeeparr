package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/demo"
)

const defaultSignInFailures = 5

func authEnv(t *testing.T) *env {
	t.Helper()
	d, err := demo.Generate(context.Background(), t.TempDir(), time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Store.Close() })
	svc := app.New(d.Store, d.Config, d.Clock.Now, d.Engine)
	s, err := New(svc, Options{Listen: "0.0.0.0:8484", Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Auth: &Auth{Username: "admin", Password: "correct horse"}, AllowedHosts: []string{"hk.example.org"}, TrustedProxy: true})
	if err != nil {
		t.Fatal(err)
	}
	return &env{h: s.Handler(), svc: svc, d: d}
}

func (e *env) req(t *testing.T, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if form != nil {
		r = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://hk.example.org")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = "hk.example.org"
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

var tokenRE = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

func token(t *testing.T, body string) string {
	t.Helper()
	m := tokenRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no csrf token on page")
	}
	return m[1]
}

func signIn(t *testing.T, e *env, pass string) *httptest.ResponseRecorder {
	t.Helper()
	lp := e.req(t, "GET", "/login", nil, nil)
	return e.req(t, "POST", "/login", url.Values{"csrf": {token(t, lp.Body.String())}, "username": {"admin"}, "password": {pass}, "next": {"/activity"}}, nil)
}

func sessionOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie (status %d)", w.Code)
	return nil
}

func TestAuthenticationGuardsEverythingButLoginHealthAndAssets(t *testing.T) {
	e := authEnv(t)
	if w := e.req(t, "GET", "/attention", nil, nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?next=%2Fattention" {
		t.Fatalf("page: %d %q", w.Code, w.Header().Get("Location"))
	}
	for _, p := range []string{"/healthz", "/static/app.css", "/login"} {
		if w := e.req(t, "GET", p, nil, nil); w.Code != 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	if w := e.req(t, "POST", "/workflows/no_progress_recovery/pause", url.Values{"note": {"x"}}, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated POST: %d", w.Code)
	}
	// The API answers HTTP Basic, for headless monitoring.
	if w := e.req(t, "GET", "/api/v1/overview", nil, nil); w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("api without credentials: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/api/v1/overview", nil)
	r.Host = "hk.example.org"
	r.SetBasicAuth("admin", "correct horse")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("api with Basic: %d", w.Code)
	}
	// Other host names are refused (DNS rebinding).
	r = httptest.NewRequest("GET", "/login", nil)
	r.Host = "evil.example"
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign host: %d", w.Code)
	}
}

func TestSignInSessionCSRFAndSignOut(t *testing.T) {
	e := authEnv(t)
	if w := signIn(t, e, "wrong"); w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
		t.Fatalf("wrong password: %d", w.Code)
	}
	w := signIn(t, e, "correct horse")
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/activity" {
		t.Fatalf("sign-in: %d %q", w.Code, w.Header().Get("Location"))
	}
	c := sessionOf(t, w)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags %+v", c)
	}
	page := e.req(t, "GET", "/activity", nil, c)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Sign out") {
		t.Fatalf("authenticated page: %d", page.Code)
	}
	tok := token(t, page.Body.String())
	// A token from another session is refused.
	other := sessionOf(t, signIn(t, e, "correct horse"))
	if w := e.req(t, "POST", "/workflows/no_progress_recovery/pause", url.Values{"csrf": {tok}, "note": {"x"}}, other); w.Code != http.StatusForbidden {
		t.Fatalf("cross-session token: %d", w.Code)
	}
	// The same session's token is accepted.
	if w := e.req(t, "POST", "/workflows/no_progress_recovery/pause", url.Values{"csrf": {tok}, "note": {"maintenance"}}, c); w.Code != http.StatusSeeOther {
		t.Fatalf("own token: %d %s", w.Code, w.Body.String())
	}
	// A tampered cookie is no session.
	bad := *c
	bad.Value = strings.Replace(bad.Value, ".", ".x", 1)
	if w := e.req(t, "GET", "/activity", nil, &bad); w.Code != http.StatusSeeOther {
		t.Fatalf("tampered cookie: %d", w.Code)
	}
	if w := e.req(t, "POST", "/logout", url.Values{"csrf": {tok}}, c); w.Code != http.StatusSeeOther || sessionOf(t, w).MaxAge >= 0 {
		t.Fatalf("logout: %d", w.Code)
	}
}

func TestSignInIsRateLimitedAndRedirectsStayLocal(t *testing.T) {
	e := authEnv(t)
	lp := e.req(t, "GET", "/login", nil, nil)
	w := e.req(t, "POST", "/login", url.Values{"csrf": {token(t, lp.Body.String())}, "username": {"admin"}, "password": {"correct horse"},
		"next": {"//evil.example/x"}}, nil)
	if w.Header().Get("Location") != "/" {
		t.Fatalf("open redirect: %q", w.Header().Get("Location"))
	}
	for i := 0; i < defaultSignInFailures; i++ {
		signIn(t, e, "wrong")
	}
	if w := signIn(t, e, "correct horse"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: %d", defaultSignInFailures, w.Code)
	}
}

func TestRemoteListenerRequirements(t *testing.T) {
	d, err := demo.Generate(context.Background(), t.TempDir(), time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Store.Close()
	svc := app.New(d.Store, d.Config, d.Clock.Now, d.Engine)
	auth := &Auth{Username: "admin", Password: "p"}
	for name, o := range map[string]Options{
		"no auth":  {Listen: "0.0.0.0:8484", AllowedHosts: []string{"h"}, TrustedProxy: true},
		"no hosts": {Listen: "0.0.0.0:8484", Auth: auth, TrustedProxy: true},
		"no TLS":   {Listen: "0.0.0.0:8484", Auth: auth, AllowedHosts: []string{"h"}},
	} {
		if _, err := New(svc, o); err == nil {
			t.Errorf("%s: remote listener accepted", name)
		}
	}
	if _, err := New(svc, Options{Listen: "0.0.0.0:8484", Auth: auth, AllowedHosts: []string{"h"}, TLSCert: "/c", TLSKey: "/k"}); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentFailedSignInsRespectGlobalLimit(t *testing.T) {
	e := authEnv(t)
	lp := e.req(t, "GET", "/login", nil, nil)
	form := url.Values{"csrf": {token(t, lp.Body.String())}, "username": {"admin"}, "password": {"wrong"}}
	start := make(chan struct{})
	const requests = 100
	statuses := make(chan int, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() {
			<-start
			statuses <- e.req(t, "POST", "/login", form, nil).Code
		})
	}
	close(start)
	wg.Wait()
	close(statuses)
	failed := 0
	for status := range statuses {
		switch status {
		case http.StatusUnauthorized:
			failed++
		case http.StatusTooManyRequests:
		default:
			t.Fatalf("unexpected sign-in status %d", status)
		}
	}
	if failed != defaultSignInFailures {
		t.Fatalf("ran %d credential checks before blocking, want %d", failed, defaultSignInFailures)
	}
}

func TestWebAuthUsesConfiguredSessionAndSignInBounds(t *testing.T) {
	a, err := newAuthState(&Auth{Username: "admin", Password: "pw", SessionTTL: time.Hour, SignInWindow: 5 * time.Minute, SignInFailures: 1}, true, "/")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	if ok, locked := a.authenticate(now, "admin", "wrong"); ok || locked {
		t.Fatal("initial failure did not match the limit")
	}
	if ok, locked := a.authenticate(now.Add(time.Minute), "admin", "pw"); ok || !locked {
		t.Fatal("configured sign-in limit was ignored")
	}
	if ok, locked := a.authenticate(now.Add(5*time.Minute), "admin", "pw"); !ok || locked {
		t.Fatal("configured sign-in hold did not expire")
	}
	value, err := a.newSession(now)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(a.cookie(value, 0))
	if _, valid := a.session(r, now.Add(59*time.Minute)); !valid {
		t.Fatal("session expired early")
	}
	if _, valid := a.session(r, now.Add(time.Hour)); valid {
		t.Fatal("session lasted beyond its configured expiry")
	}
}

// Behind a reverse proxy that shares one host name between applications
// (Ultra.cc's per-application locations, for example), everything lives
// under the base path: links, assets, redirects, and the session cookie.
func TestBasePath(t *testing.T) {
	d, err := demo.Generate(context.Background(), t.TempDir(), time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Store.Close() })
	svc := app.New(d.Store, d.Config, d.Clock.Now, d.Engine)
	s, err := New(svc, Options{Listen: "127.0.0.1:8484", Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Auth: &Auth{Username: "admin", Password: "correct horse"}, AllowedHosts: []string{"hk.example.org"}, TrustedProxy: true, BasePath: "/housekeeparr"})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{h: s.Handler(), svc: svc, d: d}
	if w := e.req(t, "GET", "/housekeeparr", nil, nil); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/housekeeparr/" {
		t.Fatalf("bare base: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := e.req(t, "GET", "/attention", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("outside the base path: %d", w.Code)
	}
	w := e.req(t, "GET", "/housekeeparr/attention", nil, nil)
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/housekeeparr/login?next=") {
		t.Fatalf("unauthenticated page: %d %q", w.Code, loc)
	}
	w = e.req(t, "GET", "/housekeeparr/login", nil, nil)
	if body := w.Body.String(); !strings.Contains(body, `action="/housekeeparr/login"`) || !strings.Contains(body, `href="/housekeeparr/static/app.css"`) {
		t.Fatalf("login page links:\n%s", body)
	}
	if w := e.req(t, "GET", "/housekeeparr/static/app.css", nil, nil); w.Code != http.StatusOK {
		t.Fatalf("asset: %d", w.Code)
	}
	w = e.req(t, "POST", "/housekeeparr/login", url.Values{"csrf": {token(t, w.Body.String())}, "username": {"admin"},
		"password": {"correct horse"}, "next": {"/attention"}}, nil)
	c := sessionOf(t, w)
	if c.Path != "/housekeeparr/" || w.Header().Get("Location") != "/housekeeparr/attention" {
		t.Fatalf("sign-in: cookie path %q, location %q", c.Path, w.Header().Get("Location"))
	}
	w = e.req(t, "GET", "/housekeeparr/", nil, c)
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `href="/housekeeparr/attention"`) || strings.Contains(body, `href="/attention"`) {
		t.Fatalf("overview %d links not under the base path", w.Code)
	}
}
