package web

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/engine"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

var pageNames = []string{"overview", "attention", "finding", "downloads", "download", "activity", "connections", "error"}

func (s *Server) funcs() template.FuncMap {
	now := s.svc.Now
	return template.FuncMap{
		"searchLabel": app.SearchLabel,
		"since": func(t time.Time) string {
			if t.IsZero() {
				return "never"
			}
			d := now().Sub(t)
			if d < 0 {
				return "in " + policy.HumanDuration(-d)
			}
			if d < 30*time.Second {
				return "just now"
			}
			return policy.HumanDuration(d) + " ago"
		},
		"sincep": func(t *time.Time) string {
			if t == nil {
				return "never"
			}
			d := now().Sub(*t)
			if d < 30*time.Second && d >= 0 {
				return "just now"
			}
			if d < 0 {
				return "in " + policy.HumanDuration(-d)
			}
			return policy.HumanDuration(d) + " ago"
		},
		"until": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			d := t.Sub(now())
			if d <= 0 {
				return "at the next poll"
			}
			return "in about " + policy.HumanDuration(d)
		},
		"iso": func(t time.Time) string { return t.UTC().Format(time.RFC3339) },
		"isop": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.UTC().Format(time.RFC3339)
		},
		"clock": func(t time.Time) string { return t.Local().Format("Jan 2, 15:04") },
		"clockp": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			return t.Local().Format("Jan 2, 15:04")
		},
		"dur":   policy.HumanDuration,
		"bytes": engine.HumanBytes,
		"sigbytes": func(sg domain.Signal[int64]) string {
			if v, ok := sg.Get(); ok {
				return engine.HumanBytes(v)
			}
			return string(sg.Status)
		},
		"sigrate": func(sg domain.Signal[int64]) string {
			if v, ok := sg.Get(); ok {
				return engine.HumanBytes(v) + "/s"
			}
			return string(sg.Status)
		},
		"sigprivacy": func(sg domain.Signal[bool]) string {
			if v, ok := sg.Get(); ok {
				if v {
					return "private"
				}
				return "public"
			}
			return string(sg.Status)
		},
		"pct": func(p *float64) string {
			if p == nil {
				return "unknown"
			}
			return strconv.FormatFloat(*p*100, 'f', 1, 64) + "%"
		},
		"pctw": func(p *float64) string {
			if p == nil {
				return "0"
			}
			v := *p * 100
			if v > 100 {
				v = 100
			}
			return strconv.FormatFloat(v, 'f', 1, 64)
		},
		"sevlabel": func(s domain.Severity) string {
			return map[domain.Severity]string{domain.SeverityError: "Error", domain.SeverityWarning: "Warning", domain.SeverityNotice: "Notice"}[s]
		},
		"stateLabel": func(s domain.FindingState) string {
			return map[domain.FindingState]string{domain.FindingOpen: "Open", domain.FindingAcknowledged: "Acknowledged",
				domain.FindingSnoozed: "Snoozed", domain.FindingResolved: "Resolved"}[s]
		},
		"assessLabel": assessLabel,
		"matchLabel":  matchLabel,
		"join":        strings.Join,
		"sevcount":    func(m map[domain.Severity]int, s string) int { return m[domain.Severity(s)] },
		"add":         func(a, b int) int { return a + b },
		"title": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
	}
}

func assessLabel(st policy.Status) string {
	return map[policy.Status]string{
		policy.StatusNotApplicable: "Not evaluated", policy.StatusNoEvidence: "No usable evidence",
		policy.StatusStale: "Evidence stale", policy.StatusGrace: "In grace period", policy.StatusObserving: "Accumulating evidence",
		policy.StatusProgressing: "Progressing", policy.StatusQualified: "Diagnosed",
	}[st]
}

func matchLabel(m string) string {
	return map[string]string{
		store.MatchMatched: "Matched", store.MatchHashOnly: "Matched by hash only", store.MatchAmbiguous: "Ambiguous",
		store.MatchNotFound: "Not in client", store.MatchClientUnavailable: "Client unavailable",
		store.MatchNotConfigured: "Client not configured", store.MatchNotTorrent: "Not a torrent",
		store.MatchNoDownloadID: "Not downloading yet", store.MatchUnknown: "Unknown",
	}[m]
}

func (s *Server) parseTemplates() error {
	s.pages = map[string]*template.Template{}
	for _, name := range pageNames {
		t, err := template.New("layout.html").Funcs(s.funcs()).ParseFS(assets, "templates/layout.html", "templates/partials.html", "templates/"+name+".html")
		if err != nil {
			return fmt.Errorf("template %s: %w", name, err)
		}
		s.pages[name] = t
	}
	return nil
}

// layoutData is shared by every page.
type layoutData struct {
	Title     string
	Nav       string
	Demo      bool
	Attention int
	Worker    *app.Worker
	Warnings  []string
	Notice    string
	CSRF      string
	Authed    bool
	Mode      string
	Now       time.Time
	Page      any
}

type pageFunc func(r *http.Request) (name, title string, data any, err error)

var errNotFound = errors.New("not found")

func (s *Server) page(fn pageFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, title, data, err := fn(r)
		ld := layoutData{Title: title, Nav: name, Demo: s.svc.Origin() == store.OriginDemo, CSRF: s.csrfToken(r), Authed: s.auth != nil,
			Mode: string(s.svc.Config().Mode), Now: s.svc.Now(), Page: data, Notice: r.URL.Query().Get("notice")}
		if c, cerr := s.svc.Overview(r.Context()); cerr == nil {
			ld.Attention = c.Counts.Open + c.Counts.Acknowledged
			ld.Worker = &c.Worker
			ld.Warnings = c.Warnings
		}
		status := http.StatusOK
		if err != nil {
			status = http.StatusInternalServerError
			msg := "Housekeeparr could not read its local state: " + err.Error()
			if errors.Is(err, app.ErrNotFound) || errors.Is(err, errNotFound) {
				status, msg = http.StatusNotFound, "That item does not exist, or it was pruned by retention."
			}
			name, ld.Title, ld.Page = "error", "Problem", msg
		}
		var buf bytes.Buffer
		if terr := s.pages[name].Execute(&buf, ld); terr != nil {
			s.log.Error("render", "page", name, "err", terr)
			http.Error(w, "internal error rendering page", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_, _ = w.Write(buf.Bytes())
	}
}

func (s *Server) overview(r *http.Request) (string, string, any, error) {
	o, err := s.svc.Overview(r.Context())
	if err != nil {
		return "", "", nil, err
	}
	top, err := s.svc.Findings(r.Context(), app.FindingQuery{States: []domain.FindingState{domain.FindingOpen, domain.FindingAcknowledged}, Limit: 5})
	if err != nil {
		return "", "", nil, err
	}
	return "overview", "Overview", struct {
		*app.Overview
		Top []app.FindingView
	}{o, top}, nil
}

func (s *Server) attention(r *http.Request) (string, string, any, error) {
	tab := r.URL.Query().Get("state")
	q := app.FindingQuery{States: []domain.FindingState{domain.FindingOpen, domain.FindingAcknowledged}}
	switch tab {
	case "snoozed":
		q.States = []domain.FindingState{domain.FindingSnoozed}
	case "resolved":
		q.States, q.Limit = []domain.FindingState{domain.FindingResolved}, 200
	default:
		tab = "active"
	}
	fs, err := s.svc.Findings(r.Context(), q)
	if err != nil {
		return "", "", nil, err
	}
	return "attention", "Needs attention", struct {
		Tab      string
		Findings []app.FindingView
	}{tab, fs}, nil
}

func (s *Server) finding(r *http.Request) (string, string, any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return "", "", nil, errNotFound
	}
	d, err := s.svc.Finding(r.Context(), id)
	if err != nil {
		return "", "", nil, err
	}
	pv, err := s.svc.Preview(r.Context(), id, action.OriginManual)
	if err != nil {
		return "", "", nil, err
	}
	return "finding", d.Title, struct {
		*app.FindingDetail
		Preview *app.ActionPreview
	}{d, pv}, nil
}

func (s *Server) downloads(r *http.Request) (string, string, any, error) {
	all := r.URL.Query().Get("all") != ""
	d, err := s.svc.Downloads(r.Context(), all)
	if err != nil {
		return "", "", nil, err
	}
	return "downloads", "Downloads", struct {
		*app.Downloads
		All bool
	}{d, all}, nil
}

func (s *Server) download(r *http.Request) (string, string, any, error) {
	d, err := s.svc.Download(r.Context(), r.PathValue("client"), r.PathValue("native"))
	if err != nil {
		return "", "", nil, err
	}
	// Newest observations first for display.
	obs := make([]store.Observation, len(d.Observations))
	for i, o := range d.Observations {
		obs[len(obs)-1-i] = o
	}
	return "download", d.Transfer.Name, struct {
		*app.DownloadDetail
		Recent []store.Observation
	}{d, obs}, nil
}

func (s *Server) activity(r *http.Request) (string, string, any, error) {
	a, err := s.svc.Activity(r.Context(), 100)
	if err != nil {
		return "", "", nil, err
	}
	return "activity", "Activity", a, nil
}

func (s *Server) connections(r *http.Request) (string, string, any, error) {
	cs, err := s.svc.Connections(r.Context())
	if err != nil {
		return "", "", nil, err
	}
	ps, err := s.svc.Policies()
	if err != nil {
		return "", "", nil, err
	}
	pauses, err := s.svc.WorkflowPauses(r.Context())
	if err != nil {
		return "", "", nil, err
	}
	type workflowControl struct {
		Workflow policy.Workflow
		Pause    *store.WorkflowPause
	}
	var wfs []workflowControl
	for _, wf := range policy.AllWorkflows() {
		c := workflowControl{Workflow: wf}
		if p, ok := pauses[string(wf)]; ok {
			c.Pause = &p
		}
		wfs = append(wfs, c)
	}
	return "connections", "Connections & policy", struct {
		Connections []app.ConnectionView
		Policies    []policy.Effective
		Workflows   []workflowControl
		Limitations []string
	}{cs, ps, wfs, app.MilestoneLimitations}, nil
}
