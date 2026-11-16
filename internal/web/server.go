// Package web is the optional interface: a thin, server-rendered client of
// the shared application service. Pages read local state only; nothing
// here contacts managers or clients.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
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
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/findings"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Options configure the server.
type Options struct {
	Listen string
	Logger *slog.Logger
}

// Server serves the interface.
type Server struct {
	svc     *app.Service
	log     *slog.Logger
	listen  string
	pages   map[string]*template.Template
	csrfKey []byte
	hosts   map[string]bool
	http    *http.Server
	static  http.Handler
}

// New builds a server. It refuses non-loopback listen addresses: remote
// exposure requires authentication, which this release does not provide.
func New(svc *app.Service, o Options) (*Server, error) {
	host, _, err := net.SplitHostPort(o.Listen)
	if err != nil {
		return nil, fmt.Errorf("listen address: %w", err)
	}
	if !isLoopback(host) {
		return nil, fmt.Errorf("refusing to listen on %q: only loopback addresses are allowed without authentication", host)
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	s := &Server{svc: svc, log: o.Logger, listen: o.Listen, csrfKey: key,
		hosts: map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, strings.Trim(host, "[]"): true}}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	sub, _ := fs.Sub(assets, "static")
	s.static = http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
	s.http = &http.Server{Addr: o.Listen, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	return s, nil
}

func isLoopback(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ListenAndServe serves until Shutdown.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	err = s.http.Serve(ln)
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
	mux.HandleFunc("POST /attempts/reset", s.resetAttempts)
	mux.HandleFunc("GET /connections", s.page(s.connections))
	mux.Handle("GET /static/", s.static)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/v1/{resource}", s.api)
	mux.HandleFunc("GET /api/v1/findings/{id}", s.apiFinding)
	mux.HandleFunc("GET /api/v1/findings/{id}/preview", s.apiPreview)
	mux.HandleFunc("GET /api/v1/downloads/{client}/{native}", s.apiDownload)
	return s.guard(mux)
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
		if !s.hostAllowed(r.Host) {
			http.Error(w, "Host not allowed. Housekeeparr only answers to loopback host names.", http.StatusMisdirectedRequest)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !s.sameOrigin(r) {
				http.Error(w, "Cross-origin request refused.", http.StatusForbidden)
				return
			}
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

func (s *Server) csrfToken() string {
	m := hmac.New(sha256.New, s.csrfKey)
	m.Write([]byte("housekeeparr-form"))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) csrfValid(r *http.Request) bool {
	got := r.PostFormValue("csrf")
	return got != "" && hmac.Equal([]byte(got), []byte(s.csrfToken()))
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
		_, _, err = s.svc.Act(ctx, id, app.ActorWeb)
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
		target += "?notice=" + url.QueryEscape(err.Error())
	case errors.As(err, new(*action.RefusedError)), errors.Is(err, app.ErrNoExecutor):
		target += "?notice=" + url.QueryEscape("Not run, nothing was sent: "+err.Error())
	case err != nil:
		s.log.Error("finding action failed", "id", id, "err", err)
		target += "?notice=" + url.QueryEscape("Could not save the change: "+err.Error())
	default:
		target += "?notice=" + url.QueryEscape("Saved.")
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
	http.Redirect(w, r, "/activity?notice="+url.QueryEscape(msg)+"#h-attempts", http.StatusSeeOther)
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
