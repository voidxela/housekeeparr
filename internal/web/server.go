// Package web is the optional interface: a thin, server-rendered client of
// the shared application service. Pages read local state only; nothing
// here contacts managers or clients.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/policy"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Options configure the server.
type Options struct {
	Listen string
	Logger *slog.Logger
	// Auth, when set, requires a session for everything but the login,
	// health and static assets (SPEC §12).
	Auth *Auth
	// AllowedHosts extends the loopback Host allowlist.
	AllowedHosts []string
	// TLSCert/TLSKey serve HTTPS; TrustedProxy declares TLS terminated by
	// a reverse proxy. A non-loopback listener needs Auth, AllowedHosts,
	// and one of them.
	TLSCert, TLSKey string
	TrustedProxy    bool
	// BasePath serves everything under a path prefix ("" for the root).
	BasePath string
	// Listener timeouts; zero uses the configuration defaults.
	ReadHeaderTimeout, ReadTimeout, WriteTimeout, IdleTimeout time.Duration
}

// OptionsFromConfig builds the server options for a configured [web]
// section; password is the resolved web.password secret ("" for none).
func OptionsFromConfig(c config.WebConfig, password string, log *slog.Logger) Options {
	o := Options{Listen: c.Listen, Logger: log, AllowedHosts: c.AllowedHosts, TLSCert: c.TLSCert, TLSKey: c.TLSKey,
		TrustedProxy: c.TrustedProxy, BasePath: c.BasePath, ReadHeaderTimeout: c.ReadHeaderTimeout.Duration, ReadTimeout: c.ReadTimeout.Duration,
		WriteTimeout: c.WriteTimeout.Duration, IdleTimeout: c.IdleTimeout.Duration}
	if c.Password != "" {
		o.Auth = &Auth{Username: c.Username, Password: password,
			SessionTTL: c.SessionTTL.Duration, SignInWindow: c.SignInWindow.Duration, SignInFailures: c.SignInFailures}
	}
	return o
}

// Server serves the interface.
type Server struct {
	svc     *app.Service
	log     *slog.Logger
	listen  string
	pages   map[string]*template.Template
	csrfKey []byte
	hosts   map[string]bool
	auth    *authState
	tlsCert string
	tlsKey  string
	// trustedProxy: a TLS-terminating proxy is declared in front.
	trustedProxy bool
	// base is the path prefix, without a trailing slash.
	base    string
	http    *http.Server
	static  http.Handler
	notices notices
}

// notices holds the outcome messages of recent form submissions. A
// redirect carries only an opaque token, so a crafted link cannot make the
// interface display arbitrary text; unknown or expired tokens show nothing.
type notices struct {
	mu    sync.Mutex
	byTok map[string]notice
	order []string
}

type notice struct {
	msg string
	at  time.Time
}

const (
	maxNotices = 128
	noticeTTL  = 10 * time.Minute
)

// flash stores msg and returns its token.
func (s *Server) flash(msg string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	tok := hex.EncodeToString(b)
	n := &s.notices
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.byTok == nil {
		n.byTok = map[string]notice{}
	}
	for len(n.order) >= maxNotices {
		delete(n.byTok, n.order[0])
		n.order = n.order[1:]
	}
	n.byTok[tok] = notice{msg: msg, at: time.Now()}
	n.order = append(n.order, tok)
	return tok
}

// noticeFor returns the message for a token, or "".
func (s *Server) noticeFor(tok string) string {
	if tok == "" {
		return ""
	}
	n := &s.notices
	n.mu.Lock()
	defer n.mu.Unlock()
	if v, ok := n.byTok[tok]; ok && time.Since(v.at) < noticeTTL {
		return v.msg
	}
	return ""
}

// New builds a server. A non-loopback listen address requires
// authentication, an explicit host allowlist, and TLS here or at a
// declared reverse proxy.
func New(svc *app.Service, o Options) (*Server, error) {
	host, _, err := net.SplitHostPort(o.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen address: %w", err)
	}
	// The same rules as configuration validation: a server built directly
	// cannot accept an exposure that validation would refuse.
	if probs := config.CheckWebExposure(config.WebExposure{Listen: o.Listen, Password: o.Auth != nil && o.Auth.Password != "",
		AllowedHosts: o.AllowedHosts, TLS: o.TLSCert != "", TrustedProxy: o.TrustedProxy}); len(probs) > 0 {
		return nil, fmt.Errorf("refusing to serve: %w", probs[0])
	}
	if o.Auth != nil && o.Auth.Password == "" {
		return nil, fmt.Errorf("authentication needs a password")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	s := &Server{svc: svc, log: o.Logger, listen: o.Listen, csrfKey: key, tlsCert: o.TLSCert, tlsKey: o.TLSKey, trustedProxy: o.TrustedProxy, base: o.BasePath,
		hosts: map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}}
	if isLoopback(host) {
		s.hosts[strings.Trim(host, "[]")] = true
	}
	for _, h := range o.AllowedHosts {
		s.hosts[strings.ToLower(h)] = true
	}
	if s.auth, err = newAuthState(o.Auth, o.TLSCert != "" || o.TrustedProxy, o.BasePath+"/"); err != nil {
		return nil, err
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	sub, _ := fs.Sub(assets, "static")
	s.static = http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	d := config.Defaults().Web
	or := func(v time.Duration, def config.Duration) time.Duration {
		if v > 0 {
			return v
		}
		return def.Duration
	}
	s.http = &http.Server{Addr: o.Listen, Handler: s.Handler(), ReadHeaderTimeout: or(o.ReadHeaderTimeout, d.ReadHeaderTimeout),
		ReadTimeout: or(o.ReadTimeout, d.ReadTimeout), WriteTimeout: or(o.WriteTimeout, d.WriteTimeout), IdleTimeout: or(o.IdleTimeout, d.IdleTimeout),
		MaxHeaderBytes: 64 << 10}
	return s, nil
}

func isLoopback(host string) bool { return config.IsLoopbackHost(strings.Trim(host, "[]")) }

// ListenAndServe serves until Shutdown.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	if s.tlsCert != "" {
		s.http.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		err = s.http.ServeTLS(ln, s.tlsCert, s.tlsKey)
	} else {
		err = s.http.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the server gracefully.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

// Handler returns the root handler (used directly by tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page(s.overview))
	mux.HandleFunc("GET /attention", s.page(s.attention))
	mux.HandleFunc("GET /findings/{id}", s.page(s.finding))
	mux.HandleFunc("POST /findings/{id}/{action}", s.findingAction)
	mux.HandleFunc("GET /downloads", s.page(s.downloads))
	mux.HandleFunc("GET /downloads/{client}/{native}", s.page(s.download))
	mux.HandleFunc("GET /activity", s.page(s.activity))
	mux.HandleFunc("GET /sources", s.page(s.sources))
	mux.HandleFunc("GET /coverage", s.page(s.coverage))
	mux.HandleFunc("POST /attempts/reset", s.resetAttempts)
	mux.HandleFunc("POST /workflows/{workflow}/{verb}", s.workflowControl)
	mux.HandleFunc("GET /connections", s.page(s.connections))
	mux.Handle("GET /static/", s.static)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /login", s.login)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /api/v1/{resource}", s.api)
	mux.HandleFunc("GET /api/v1/findings/{id}", s.apiFinding)
	mux.HandleFunc("GET /api/v1/findings/{id}/preview", s.apiPreview)
	mux.HandleFunc("GET /api/v1/downloads/{client}/{native}", s.apiDownload)
	// Host and origin checks and security headers apply to every response,
	// including the base path's redirect and 404s outside it; the session
	// check sees the path with the mount prefix stripped.
	return s.guard(s.prefixed(s.authorized(mux)))
}

// authorized requires a session (or API credentials) where configured.
func (s *Server) authorized(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authorize(w, r) {
			next.ServeHTTP(w, r)
		}
	})
}

// prefixed serves next under the base path: it strips the prefix from
// requests and adds it to every local redirect, so handlers and their
// redirects stay root-relative.
func (s *Server) prefixed(next http.Handler) http.Handler {
	if s.base == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == s.base:
			http.Redirect(w, r, s.base+"/", http.StatusMovedPermanently)
			return
		case !strings.HasPrefix(p, s.base+"/"):
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = strings.TrimPrefix(p, s.base)
		r2.URL.RawPath = ""
		next.ServeHTTP(&prefixWriter{ResponseWriter: w, base: s.base}, r2)
	})
}

// prefixWriter adds the base path to local Location headers.
type prefixWriter struct {
	http.ResponseWriter
	base string
}

func (p *prefixWriter) WriteHeader(code int) {
	h := p.ResponseWriter.Header()
	if loc := h.Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") && !mounted(loc, p.base) {
		h.Set("Location", p.base+loc)
	}
	p.ResponseWriter.WriteHeader(code)
}

// mounted reports whether a local path already carries the base path, so
// a redirect is prefixed exactly once.
func mounted(loc, base string) bool {
	return loc == base || strings.HasPrefix(loc, base+"/") || strings.HasPrefix(loc, base+"?") || strings.HasPrefix(loc, base+"#")
}

// guard enforces Host allowlisting (DNS-rebinding protection), security
// headers, and same-origin checks for state-changing requests.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if s.tlsCert != "" {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if !s.hostAllowed(r.Host) {
			http.Error(w, "Host not allowed. Housekeeparr only answers to loopback host names and web.allowed_hosts.", http.StatusMisdirectedRequest)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !s.sameOrigin(r) {
				msg := "Cross-origin request refused."
				if s.trustedProxy {
					msg += " Behind a reverse proxy, pass the original Host header, with its port, to Housekeeparr (nginx: proxy_set_header Host $http_host); this request's Host is " +
						strconv.Quote(r.Host) + "."
				}
				http.Error(w, msg, http.StatusForbidden)
				return
			}
		}
		if s.auth != nil {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return s.hosts[strings.Trim(strings.ToLower(host), "[]")]
}

func (s *Server) sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		ref := r.Header.Get("Referer")
		if ref == "" {
			// Without Origin or Referer, the CSRF token is the remaining check.
			return origin != "null"
		}
		origin = ref
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

// csrfToken is bound to the session when authentication is on, so a
// token from one session is useless in another.
func (s *Server) csrfToken(r *http.Request) string {
	m := hmac.New(sha256.New, s.csrfKey)
	m.Write([]byte("housekeeparr-form"))
	if s.auth != nil {
		nonce, _ := s.auth.session(r, time.Now())
		m.Write([]byte("|" + nonce))
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) csrfValid(r *http.Request) bool {
	got := r.PostFormValue("csrf")
	return got != "" && hmac.Equal([]byte(got), []byte(s.csrfToken(r)))
}

func (s *Server) findingAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil || !s.csrfValid(r) {
		http.Error(w, "Form expired or invalid; reload the page and try again.", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	switch r.PathValue("action") {
	case "ack":
		_, err = s.svc.Acknowledge(ctx, id, app.ActorWeb)
	case "reopen":
		_, err = s.svc.Reopen(ctx, id, app.ActorWeb)
	case "act":
		if r.PostFormValue("confirm") != "yes" {
			err = errors.New("confirm the previewed effects before running the action")
			break
		}
		// The action outlives the request: a browser or proxy giving up
		// must not stop a workflow between its journaled writes.
		_, _, err = s.svc.Act(context.WithoutCancel(ctx), id, app.ActorWeb)
	case "snooze":
		var d time.Duration
		d, err = time.ParseDuration(r.PostFormValue("duration"))
		if err == nil {
			_, err = s.svc.Snooze(ctx, id, d, app.ActorWeb)
		}
	default:
		http.NotFound(w, r)
		return
	}
	target := fmt.Sprintf("/findings/%d", id)
	switch {
	case errors.Is(err, app.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, findings.ErrResolved), errors.Is(err, findings.ErrInvalidSnooze):
		target += "?notice=" + s.flash(err.Error())
	case errors.As(err, new(*action.RefusedError)), errors.Is(err, app.ErrNoExecutor):
		target += "?notice=" + s.flash("Not run, nothing was sent: "+err.Error())
	case err != nil:
		s.log.Error("finding action failed", "id", id, "err", err)
		target += "?notice=" + s.flash("Could not save the change: "+err.Error())
	default:
		target += "?notice=" + s.flash("Saved.")
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// resetAttempts allows further recovery attempts for one episode or movie
// after review (the same service as "housekeeparr attempts reset"). A note
// and an explicit confirmation are required; nothing is sent externally.
func (s *Server) resetAttempts(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil || !s.csrfValid(r) {
		http.Error(w, "Form expired or invalid; reload the page and try again.", http.StatusForbidden)
		return
	}
	ref := r.PostFormValue("ref")
	var err error
	if r.PostFormValue("confirm") != "yes" {
		err = errors.New("confirm that you reviewed why earlier replacements did not resolve this content")
	} else {
		err = s.svc.ResetAttempts(r.Context(), ref, app.ActorWeb, r.PostFormValue("note"))
	}
	msg := "Recovery attempts for " + ref + " were reset; earlier attempts remain in the history. Nothing was sent."
	if err != nil {
		msg = "Not reset: " + err.Error()
	}
	http.Redirect(w, r, "/activity?notice="+s.flash(msg)+"#h-attempts", http.StatusSeeOther)
}

// workflowControl pauses or resumes one workflow (the same service as the
// CLI's pause/resume); a note is required.
func (s *Server) workflowControl(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil || !s.csrfValid(r) {
		http.Error(w, "Form expired or invalid; reload the page and try again.", http.StatusForbidden)
		return
	}
	wf, note := policy.Workflow(r.PathValue("workflow")), r.PostFormValue("note")
	var err error
	msg := string(wf) + " "
	switch r.PathValue("verb") {
	case "pause":
		err, msg = s.svc.PauseWorkflow(r.Context(), wf, app.ActorWeb, note), msg+"paused: nothing more is sent for it, automatically or manually, until it is resumed. Requests already accepted may still complete."
	case "resume":
		err, msg = s.svc.ResumeWorkflow(r.Context(), wf, app.ActorWeb, note), msg+"resumed."
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		msg = "Not changed: " + err.Error()
	}
	http.Redirect(w, r, "/connections?notice="+s.flash(msg)+"#h-workflows", http.StatusSeeOther)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	wk, err := s.svc.Worker(r.Context())
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "error": err.Error()})
		return
	}
	if !wk.Healthy() {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": wk.State, "explanation": wk.Explanation, "origin": s.svc.Origin()})
}

func writeAPI(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(err, app.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	switch r.PathValue("resource") {
	case "overview":
		v, err := s.svc.Overview(ctx)
		writeAPI(w, v, err)
	case "findings":
		q := app.FindingQuery{}
		if r.URL.Query().Get("all") != "" {
			q.States = append(append(q.States, app.ActiveStates...), "resolved")
		}
		v, err := s.svc.Findings(ctx, q)
		writeAPI(w, v, err)
	case "downloads":
		v, err := s.svc.Downloads(ctx, r.URL.Query().Get("all") != "")
		writeAPI(w, v, err)
	case "connections":
		v, err := s.svc.Connections(ctx)
		writeAPI(w, v, err)
	case "policy":
		v, err := s.svc.Policies()
		writeAPI(w, v, err)
	case "activity":
		v, err := s.svc.Activity(ctx, 100)
		writeAPI(w, v, err)
	default:
		writeAPI(w, nil, app.ErrNotFound)
	}
}

func (s *Server) apiFinding(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAPI(w, nil, app.ErrNotFound)
		return
	}
	v, err := s.svc.Finding(r.Context(), id)
	writeAPI(w, v, err)
}

func (s *Server) apiPreview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAPI(w, nil, app.ErrNotFound)
		return
	}
	origin := action.OriginManual
	if r.URL.Query().Get("origin") == "automatic" {
		origin = action.OriginAutomatic
	}
	v, err := s.svc.Preview(r.Context(), id, origin)
	writeAPI(w, v, err)
}

func (s *Server) apiDownload(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Download(r.Context(), r.PathValue("client"), r.PathValue("native"))
	writeAPI(w, v, err)
}
