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
	"github.com/voidxela/housekeeparr/internal/store"
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

func TestFindingPageShowsActionPreviewAndActRefuses(t *testing.T) {
	e := setup(t)
	f := firstFinding(t, e, domain.FindingTransferStalled)
	page := e.get(t, "/findings/"+itoa(f.ID)).Body.String()
	for _, want := range []string{"Action preview: no_progress_recovery", "Not eligible", "tested write workflow", "FAILS",
		"manager.queue.fail", `&#34;removeFromClient&#34;:false`, "housekeeparr preview " + itoa(f.ID)} {
		if !strings.Contains(page, want) {
			t.Errorf("finding page lacks %q", want)
		}
	}
	if strings.Contains(page, "Run this action") {
		t.Error("ineligible preview offers the action button")
	}
	m := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	path := "/findings/" + itoa(f.ID) + "/act"
	// Without confirmation nothing runs.
	w := e.do(t, post(path, "http://127.0.0.1:8485", url.Values{"csrf": {m[1]}}))
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "confirm") {
		t.Fatalf("unconfirmed act: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Confirmed, it goes through the coordinator and is refused with reasons.
	w = e.do(t, post(path, "http://127.0.0.1:8485", url.Values{"csrf": {m[1]}, "confirm": {"yes"}}))
	loc, _ := url.QueryUnescape(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || !strings.Contains(loc, "nothing was sent") || !strings.Contains(loc, "tested write workflow") {
		t.Fatalf("confirmed act: %d %s", w.Code, loc)
	}
	if acts, _ := e.svc.Actions(context.Background(), 10); len(acts) != 0 {
		t.Fatalf("refused action journaled: %+v", acts)
	}
	// The JSON preview is the same evaluation.
	w = e.get(t, "/api/v1/findings/"+itoa(f.ID)+"/preview?origin=automatic")
	var pv app.ActionPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &pv) != nil || pv.Proposal == nil || pv.Proposal.Origin != "automatic" || pv.Proposal.Eligible() {
		t.Fatalf("preview API: %d %s", w.Code, w.Body.String())
	}
}

// Activity shows the replacement search and the replacement grab of a
// recovery as separate outcomes, with their notes.
func TestActivityShowsSearchAndGrabOutcomesSeparately(t *testing.T) {
	e := setup(t)
	now := e.d.Clock.Now()
	var id int64
	if err := e.d.Store.Write(context.Background(), func(tx *store.Tx) error {
		var err error
		id, err = tx.CreateIntent(store.Intent{Key: "web-outcome", Workflow: "no_progress_recovery", Origin: "automatic",
			Explanation: "recovery for the render test"}, now)
		if err != nil {
			return err
		}
		if err := tx.SetIntentState(id, store.IntentExecuting, "", now); err != nil {
			return err
		}
		if err := tx.SetIntentState(id, store.IntentSucceeded, "every step verified", now); err != nil {
			return err
		}
		if err := tx.CreateOutcome(store.RecoveryOutcome{IntentID: id, ManagerID: "sonarr", Content: []string{"episode:1"}, OldDownloadID: "aa",
			StartedAt: now, Search: store.SearchOutcome{Since: &now}}); err != nil {
			return err
		}
		cmd := int64(45)
		return tx.SaveSearchOutcome(id, store.SearchOutcome{State: store.SearchCompleted, Since: &now, CommandID: &cmd, Name: "EpisodeSearch",
			Note: "the manager's EpisodeSearch (command 45) for this content completed. A completed search is not a grab"})
	}); err != nil {
		t.Fatal(err)
	}
	w := e.get(t, "/activity")
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "<no value>") {
		t.Fatalf("status %d\n%s", w.Code, body)
	}
	for _, want := range []string{"Replacement search:", "completed", "command 45", "A completed search is not a grab", "Replacement grab:", "awaiting_grab"} {
		if !strings.Contains(body, want) {
			t.Errorf("activity page lacks %q", want)
		}
	}
}

// Recovery attempts per content are listed on Activity with a reset
// control: the reset needs CSRF, a note, and explicit confirmation, and
// uses the same service as the CLI.
func TestActivityListsAttemptsAndResetsThemWithReview(t *testing.T) {
	e := setup(t)
	now := e.d.Clock.Now()
	scope := "attempts:demo-sonarr|episode:42"
	if err := e.d.Store.Write(context.Background(), func(tx *store.Tx) error {
		for i := 0; i < 3; i++ {
			id, err := tx.CreateIntent(store.Intent{Key: "web-att-" + itoa(int64(i)), Workflow: "no_progress_recovery", Origin: "automatic"}, now)
			if err != nil {
				return err
			}
			if err := tx.ReserveBudget(id, store.Budget{Kind: store.BudgetAttempts, Scope: scope, Limit: 3}, 1, now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page := e.get(t, "/activity").Body.String()
	for _, want := range []string{"demo-sonarr|episode:42", "3 of 3", "needs review", "does not mean no release exists"} {
		if !strings.Contains(page, want) {
			t.Fatalf("activity lacks %q", want)
		}
	}
	m := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token")
	}
	used := func() int {
		u, err := e.d.Store.BudgetUsage(context.Background(), store.Budget{Kind: store.BudgetAttempts, Scope: scope, Limit: 3}, 1, e.d.Clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		return u.Used
	}
	form := url.Values{"csrf": {m[1]}, "ref": {"demo-sonarr|episode:42"}, "note": {"checked history: release fake"}}
	if w := e.do(t, post("/attempts/reset", "https://attacker.example", form)); w.Code != 403 || used() != 3 {
		t.Fatalf("cross-origin reset: %d, used %d", w.Code, used())
	}
	w := e.do(t, post("/attempts/reset", "http://127.0.0.1:8485", form))
	if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "Not+reset") || used() != 3 {
		t.Fatalf("reset without confirmation: %d %s used %d", w.Code, w.Header().Get("Location"), used())
	}
	noNote := url.Values{"csrf": {m[1]}, "ref": {"demo-sonarr|episode:42"}, "confirm": {"yes"}}
	if w := e.do(t, post("/attempts/reset", "http://127.0.0.1:8485", noNote)); used() != 3 || !strings.Contains(w.Header().Get("Location"), "Not+reset") {
		t.Fatalf("reset without a note: used %d", used())
	}
	form.Set("confirm", "yes")
	if w := e.do(t, post("/attempts/reset", "http://127.0.0.1:8485", form)); w.Code != http.StatusSeeOther || used() != 0 {
		t.Fatalf("confirmed reset: %d used %d", w.Code, used())
	}
	if page := e.get(t, "/activity").Body.String(); strings.Contains(page, "needs review") {
		t.Fatal("reset content still shown as needing review")
	}
}

// Workflow controls pause and resume through the shared service, with CSRF
// and a required note; the pause is shown with who paused it and why.
func TestWorkflowPauseControls(t *testing.T) {
	e := setup(t)
	page := e.get(t, "/connections").Body.String()
	m := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, "Workflow controls") {
		t.Fatal("no workflow controls")
	}
	path := "/workflows/no_progress_recovery/pause"
	if w := e.do(t, post(path, "https://attacker.example", url.Values{"csrf": {m[1]}, "note": {"x"}})); w.Code != 403 {
		t.Fatalf("cross-origin pause: %d", w.Code)
	}
	if w := e.do(t, post(path, "http://127.0.0.1:8485", url.Values{"csrf": {m[1]}})); !strings.Contains(w.Header().Get("Location"), "Not+changed") {
		t.Fatalf("pause without note: %s", w.Header().Get("Location"))
	}
	if w := e.do(t, post(path, "http://127.0.0.1:8485", url.Values{"csrf": {m[1]}, "note": {"tracker maintenance"}})); w.Code != http.StatusSeeOther {
		t.Fatalf("pause: %d", w.Code)
	}
	if ps, _ := e.svc.WorkflowPauses(context.Background()); ps["no_progress_recovery"].Note != "tracker maintenance" {
		t.Fatalf("pauses %v", ps)
	}
	page = e.get(t, "/connections").Body.String()
	if !strings.Contains(page, "tracker maintenance") || !strings.Contains(page, "/workflows/no_progress_recovery/resume") {
		t.Fatal("pause not shown with a resume control")
	}
	if w := e.do(t, post("/workflows/no_progress_recovery/resume", "http://127.0.0.1:8485", url.Values{"csrf": {m[1]}, "note": {"done"}})); w.Code != http.StatusSeeOther {
		t.Fatalf("resume: %d", w.Code)
	}
	if ps, _ := e.svc.WorkflowPauses(context.Background()); len(ps) != 0 {
		t.Fatalf("still paused: %v", ps)
	}
}
