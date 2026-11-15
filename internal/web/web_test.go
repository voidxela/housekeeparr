package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/domain"
)

type env struct {
	h   http.Handler
	svc *app.Service
	d   *demo.Demo
}

func setup(t *testing.T) *env {
	t.Helper()
	d, err := demo.Generate(context.Background(), t.TempDir(), time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Store.Close() })
	svc := app.New(d.Store, d.Config, d.Clock.Now, d.Engine)
	s, err := New(svc, Options{Listen: "127.0.0.1:0", Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	return &env{h: s.Handler(), svc: svc, d: d}
}

func (e *env) do(t *testing.T, r *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	if r.Host == "" || r.Host == "example.com" {
		r.Host = "127.0.0.1:8485"
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func (e *env) get(t *testing.T, path string) *httptest.ResponseRecorder {
	return e.do(t, httptest.NewRequest("GET", path, nil))
}

func firstFinding(t *testing.T, e *env, typ string) domain.Finding {
	t.Helper()
	fs, err := e.svc.Findings(context.Background(), app.FindingQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Type == typ {
			return f.Finding
		}
	}
	t.Fatalf("no %s finding in demo data", typ)
	return domain.Finding{}
}

func TestEveryPageRendersWithDemoBanner(t *testing.T) {
	e := setup(t)
	f := firstFinding(t, e, domain.FindingTransferStalled)
	pages := []string{"/", "/attention", "/attention?state=snoozed", "/attention?state=resolved", "/downloads", "/downloads?all=1",
		"/findings/" + itoa(f.ID), "/downloads/" + f.Subject.ClientID + "/" + f.Subject.NativeID, "/activity", "/connections"}
	for _, p := range pages {
		w := e.get(t, p)
		if w.Code != 200 {
			t.Fatalf("%s: status %d\n%s", p, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "Demo data.") {
			t.Errorf("%s: missing demo banner", p)
		}
		if strings.Contains(body, "<no value>") || strings.Contains(body, "%!") {
			t.Errorf("%s: template rendering artefact", p)
		}
		if regexp.MustCompile(`\sstyle="`).MatchString(body) || strings.Contains(body, "<script>") {
			t.Errorf("%s: inline style or script would violate CSP", p)
		}
	}
	if w := e.get(t, "/findings/999999"); w.Code != 404 {
		t.Fatalf("missing finding: %d", w.Code)
	}
}

func TestSuspendedStallExplainedInUI(t *testing.T) {
	e := setup(t)
	fs, _ := e.svc.Findings(context.Background(), app.FindingQuery{})
	for _, f := range fs {
		if f.Type == domain.FindingTransferStalled && strings.Contains(f.Title, "Northern.Line") {
			body := e.get(t, "/findings/"+itoa(f.ID)).Body.String()
			if !strings.Contains(body, "Since then:") || !strings.Contains(body, "paused") {
				t.Fatal("paused-after-diagnosis stall must explain the change")
			}
			return
		}
	}
	t.Fatal("demo data should include a stall that was paused after diagnosis")
}

func TestHostHeaderAllowlist(t *testing.T) {
	e := setup(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "attacker.example:8485"
	if w := e.do(t, r); w.Code != http.StatusMisdirectedRequest {
		t.Fatalf("DNS-rebinding host accepted: %d", w.Code)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.Host = "localhost:8485"
	if w := e.do(t, r); w.Code != 200 {
		t.Fatalf("localhost refused: %d", w.Code)
	}
}

func post(path, origin string, form url.Values) *http.Request {
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Host = "127.0.0.1:8485"
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestMutationsRequireSameOriginAndCSRF(t *testing.T) {
	e := setup(t)
	f := firstFinding(t, e, domain.FindingTransferStalled)
	path := "/findings/" + itoa(f.ID) + "/ack"
	page := e.get(t, "/findings/"+itoa(f.ID)).Body.String()
	m := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token in form")
	}
	if w := e.do(t, post(path, "http://127.0.0.1:8485", url.Values{})); w.Code != 403 {
		t.Fatalf("missing token accepted: %d", w.Code)
	}
	if w := e.do(t, post(path, "https://attacker.example", url.Values{"csrf": {m[1]}})); w.Code != 403 {
		t.Fatalf("cross-origin accepted: %d", w.Code)
	}
	r := post(path, "", url.Values{"csrf": {m[1]}})
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if w := e.do(t, r); w.Code != 403 {
		t.Fatalf("cross-site fetch accepted: %d", w.Code)
	}
	w := e.do(t, post(path, "http://127.0.0.1:8485", url.Values{"csrf": {m[1]}}))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("valid acknowledge: %d %s", w.Code, w.Body.String())
	}
	got, _ := e.svc.Finding(context.Background(), f.ID)
	if got.State != domain.FindingAcknowledged {
		t.Fatalf("state %s", got.State)
	}
	// Snooze with an out-of-range duration is refused with an explanation.
	w = e.do(t, post("/findings/"+itoa(f.ID)+"/snooze", "http://127.0.0.1:8485", url.Values{"csrf": {m[1]}, "duration": {"1m"}}))
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "notice=") {
		t.Fatalf("bad snooze: %d %s", w.Code, w.Header().Get("Location"))
	}
	if got, _ := e.svc.Finding(context.Background(), f.ID); got.State != domain.FindingAcknowledged {
		t.Fatal("invalid snooze changed the finding")
	}
}

func TestJSONAPIAndHealth(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/api/v1/overview", "/api/v1/findings", "/api/v1/findings?all=1", "/api/v1/downloads", "/api/v1/connections", "/api/v1/policy", "/api/v1/activity"} {
		w := e.get(t, p)
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	var ov app.Overview
	_ = json.Unmarshal(e.get(t, "/api/v1/overview").Body.Bytes(), &ov)
	if ov.Origin != "demo" {
		t.Fatalf("API must expose data origin, got %q", ov.Origin)
	}
	if w := e.get(t, "/api/v1/nope"); w.Code != 404 {
		t.Fatalf("unknown resource %d", w.Code)
	}
	w := e.get(t, "/healthz")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"running"`) {
		t.Fatalf("healthz %d %s", w.Code, w.Body.String())
	}
}

func TestRefusesNonLoopbackListen(t *testing.T) {
	if _, err := New(nil, Options{Listen: "0.0.0.0:8484"}); err == nil {
		t.Fatal("non-loopback listen must be refused")
	}
}

func itoa(n int64) string    { return strings.TrimSpace(strings.Replace(jsonNum(n), "\n", "", -1)) }
func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }

type testWriter struct{ t *testing.T }

func (w testWriter) Write(b []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(b)))
	return len(b), nil
}

var _ = io.Discard
