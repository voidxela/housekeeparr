package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/config"
)

// Auth enables authentication (SPEC §12). Without it the interface is
// loopback-only.
type Auth struct {
	Username       string
	Password       string
	SessionTTL     time.Duration
	SignInWindow   time.Duration
	SignInFailures int
}

const sessionCookie = "hk_session"

type authState struct {
	user, pass     [32]byte // digests, compared in constant time
	key            []byte   // signs session cookies (per process)
	secure         bool     // Secure cookies (TLS here or at a trusted proxy)
	path           string   // cookie path: the base path with a trailing slash
	sessionTTL     time.Duration
	signInWindow   time.Duration
	signInFailures int

	mu       sync.Mutex
	failures []time.Time
}

func newAuthState(a *Auth, secure bool, path string) (*authState, error) {
	if a == nil {
		return nil, nil
	}
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	defaults := config.Defaults().Web
	s := &authState{user: sha256.Sum256([]byte(a.Username)), pass: sha256.Sum256([]byte(a.Password)), key: k, secure: secure, path: path,
		sessionTTL: a.SessionTTL, signInWindow: a.SignInWindow, signInFailures: a.SignInFailures}
	if s.sessionTTL <= 0 {
		s.sessionTTL = defaults.SessionTTL.Duration
	}
	if s.signInWindow <= 0 {
		s.signInWindow = defaults.SignInWindow.Duration
	}
	if s.signInFailures <= 0 {
		s.signInFailures = defaults.SignInFailures
	}
	return s, nil
}

func (a *authState) credentialsOK(user, pass string) bool {
	u, p := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return subtle.ConstantTimeCompare(u[:], a.user[:])&subtle.ConstantTimeCompare(p[:], a.pass[:]) == 1
}

// authenticate checks the shared limit and records a failure atomically.
func (a *authState) authenticate(now time.Time, user, pass string) (ok, locked bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	keep := a.failures[:0]
	for _, t := range a.failures {
		if now.Sub(t) < a.signInWindow {
			keep = append(keep, t)
		}
	}
	a.failures = keep
	if len(a.failures) >= a.signInFailures {
		return false, true
	}
	if !a.credentialsOK(user, pass) {
		a.failures = append(a.failures, now)
		return false, false
	}
	return true, false
}

func (a *authState) sign(payload string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

// newSession returns a cookie value "expiry.nonce.mac".
func (a *authState) newSession(now time.Time) (string, error) {
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		return "", err
	}
	p := strconv.FormatInt(now.Add(a.sessionTTL).Unix(), 10) + "." + base64.RawURLEncoding.EncodeToString(n)
	return p + "." + a.sign(p), nil
}

// session returns the session nonce of a valid, unexpired cookie.
func (a *authState) session(r *http.Request, now time.Time) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return "", false
	}
	p := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(a.sign(p))) {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || now.Unix() >= exp {
		return "", false
	}
	return parts[1], true
}

func (a *authState) cookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: a.path, MaxAge: maxAge, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode}
}

// public paths need no session.
func public(r *http.Request) bool {
	p := r.URL.Path
	return p == "/login" || p == "/healthz" || strings.HasPrefix(p, "/static/")
}

// authorize lets a request through, or answers it: a page redirects to
// the login, the API asks for Basic credentials, and anything else is
// refused.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	a := s.auth
	if a == nil || public(r) {
		return true
	}
	if _, ok := a.session(r, time.Now()); ok {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if u, p, ok := r.BasicAuth(); ok && r.Method == http.MethodGet {
			if authenticated, _ := a.authenticate(time.Now(), u, p); authenticated {
				return true
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="Housekeeparr", charset="UTF-8"`)
		http.Error(w, "Authentication required.", http.StatusUnauthorized)
		return false
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return false
	}
	http.Error(w, "Authentication required; sign in again.", http.StatusUnauthorized)
	return false
}

// localNext keeps a post-login redirect on this site.
func localNext(n string) string {
	if !strings.HasPrefix(n, "/") || strings.HasPrefix(n, "//") || strings.Contains(n, "\\") {
		return "/"
	}
	return n
}

var loginPage = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark"><title>Sign in · Housekeeparr</title>
<link rel="stylesheet" href="{{.Base}}/static/app.css"><link rel="icon" href="{{.Base}}/static/icon.svg" type="image/svg+xml"></head>
<body><main id="main" class="login">
<section class="card" aria-labelledby="h-login">
<h1 id="h-login"><img src="{{.Base}}/static/icon.svg" alt="" width="22" height="22"> Housekeeparr</h1>
{{if .Error}}<div class="banner banner-error" role="alert"><span class="glyph" aria-hidden="true">■</span> <span>{{.Error}}</span></div>{{end}}
<form method="post" action="{{.Base}}/login">
<input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="next" value="{{.Next}}">
<p><label for="user">Username</label><br><input id="user" name="username" autocomplete="username" required></p>
<p><label for="pass">Password</label><br><input id="pass" name="password" type="password" autocomplete="current-password" required></p>
<p><button type="submit">Sign in</button></p>
</form></section></main></body></html>`))

func (s *Server) loginToken() string {
	m := hmac.New(sha256.New, s.csrfKey)
	m.Write([]byte("housekeeparr-login"))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	render := func(status int, msg, next string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_ = loginPage.Execute(w, map[string]string{"CSRF": s.loginToken(), "Next": localNext(next), "Error": msg, "Base": s.base})
	}
	if r.Method == http.MethodGet {
		render(http.StatusOK, "", r.URL.Query().Get("next"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil || !hmac.Equal([]byte(r.PostFormValue("csrf")), []byte(s.loginToken())) {
		render(http.StatusForbidden, "The form expired; try again.", "/")
		return
	}
	next := localNext(r.PostFormValue("next"))
	now := time.Now()
	authenticated, locked := s.auth.authenticate(now, r.PostFormValue("username"), r.PostFormValue("password"))
	if locked {
		render(http.StatusTooManyRequests, "Too many failed sign-ins; try again in a few minutes.", next)
		return
	}
	if !authenticated {
		s.log.Warn("failed sign-in", "remote", r.RemoteAddr)
		render(http.StatusUnauthorized, "Wrong username or password.", next)
		return
	}
	v, err := s.auth.newSession(now)
	if err != nil {
		render(http.StatusInternalServerError, "Could not start a session.", next)
		return
	}
	http.SetCookie(w, s.auth.cookie(v, int(s.auth.sessionTTL.Seconds())))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil || !s.csrfValid(r) {
		http.Error(w, "Form expired or invalid; reload the page and try again.", http.StatusForbidden)
		return
	}
	if s.auth != nil {
		http.SetCookie(w, s.auth.cookie("", -1))
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
