package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/store"
)

var t0 = time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)

type sink struct {
	mu     sync.Mutex
	status int
	got    []*http.Request
	bodies []string
}

func (s *sink) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.got, s.bodies = append(s.got, r), append(s.bodies, string(b))
		st := s.status
		s.mu.Unlock()
		if st == 0 {
			st = 200
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func open(t *testing.T, dir string) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), dir, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func finding(id int64, sev domain.Severity) domain.Finding {
	return domain.Finding{ID: id, Severity: sev, State: domain.FindingOpen, Title: "No verified progress: Show.S01E01", Summary: "No verified progress for 2h.",
		Subject: domain.Subject{Connections: []string{"qbit", "sonarr"}}, Detail: domain.FindingDetail{NextSteps: []string{"Inspect the torrent."}}}
}

func enqueue(t *testing.T, st *store.Store, tg []config.NotifyConfig, res findings.Result, now time.Time) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *store.Tx) error { return Enqueue(tx, tg, config.Defaults().Notifications, res, now) }); err != nil {
		t.Fatal(err)
	}
}

func target(max int) config.NotifyConfig {
	return config.NotifyConfig{ID: "hook", Format: "json", MinSeverity: "warning", MaxPerHour: max, LinkBase: "https://hk.example"}
}

func TestOpenedAndResolvedFindingsAreDelivered(t *testing.T) {
	ctx := context.Background()
	st := open(t, t.TempDir())
	defer st.Close()
	var s sink
	srv := s.server(t)
	tg := []config.NotifyConfig{target(10)}
	urls := map[string]string{"hook": srv.URL + "/secret-token"}
	enqueue(t, st, tg, findings.Result{Opened: []domain.Finding{finding(1, domain.SeverityWarning), finding(2, domain.SeverityNotice)}}, t0)
	// Resolution of a finding never delivered is not notified.
	enqueue(t, st, tg, findings.Result{Resolved: []domain.Finding{finding(3, domain.SeverityWarning)}}, t0)
	rep, err := Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, t0)
	if err != nil || rep.Sent != 1 {
		t.Fatalf("rep %+v err %v (notice is below min_severity)", rep, err)
	}
	var p payload
	if err := json.Unmarshal([]byte(s.bodies[0]), &p); err != nil || p.Kind != KindOpened || p.Link != "https://hk.example/findings/1" ||
		!strings.Contains(p.Body, "Next step: Inspect") || *p.FindingID != 1 {
		t.Fatalf("payload %+v %v", p, err)
	}
	// The same change is not queued twice.
	enqueue(t, st, tg, findings.Result{Opened: []domain.Finding{finding(1, domain.SeverityWarning)}}, t0)
	r := finding(1, domain.SeverityWarning)
	r.Resolution = "Verified: progress resumed."
	enqueue(t, st, tg, findings.Result{Resolved: []domain.Finding{r}}, t0.Add(time.Minute))
	rep, _ = Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, t0.Add(time.Minute))
	if rep.Sent != 1 || len(s.bodies) != 2 || !strings.Contains(s.bodies[1], "Verified: progress resumed.") {
		t.Fatalf("rep %+v bodies %v", rep, s.bodies)
	}
}

// Many findings at once (a shared outage) become one digest.
func TestNotificationsExpireWhenTargetIsUnavailableOrRemoved(t *testing.T) {
	for _, tc := range []struct {
		name    string
		targets []config.NotifyConfig
	}{
		{name: "no webhook URL", targets: []config.NotifyConfig{target(10)}},
		{name: "target removed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := open(t, t.TempDir())
			defer st.Close()
			enqueue(t, st, []config.NotifyConfig{target(10)}, findings.Result{Opened: []domain.Finding{finding(1, domain.SeverityWarning)}}, t0)
			if r, err := Deliver(ctx, st, tc.targets, config.Defaults().Notifications, nil, nil, t0.Add(23*time.Hour)); err != nil || r.Dropped != 0 {
				t.Fatalf("event dropped early: %+v, %v", r, err)
			}
			if r, err := Deliver(ctx, st, tc.targets, config.Defaults().Notifications, nil, nil, t0.Add(25*time.Hour)); err != nil || r.Dropped != 1 {
				t.Fatalf("old event was not recorded as expired: %+v, %v", r, err)
			}
			rows, err := st.RecentNotifications(ctx, 1)
			if err != nil || len(rows) != 1 || rows[0].DroppedAt == nil || rows[0].Attempts != 0 {
				t.Fatalf("outbox after expiry: %+v, %v", rows, err)
			}
		})
	}
}

func TestBurstBecomesDigest(t *testing.T) {
	st := open(t, t.TempDir())
	defer st.Close()
	var fs []domain.Finding
	for i := int64(1); i <= 12; i++ {
		fs = append(fs, finding(i, domain.SeverityWarning))
	}
	enqueue(t, st, []config.NotifyConfig{target(10)}, findings.Result{Opened: fs}, t0)
	n, _ := st.PendingNotifications(context.Background(), "hook")
	due, _ := st.DueNotifications(context.Background(), "hook", t0, 10)
	if n != 1 || due[0].Kind != KindDigest || !strings.Contains(due[0].Body, "and 7 more") {
		t.Fatalf("pending %d %+v", n, due)
	}
}

// The hourly limit survives a restart; held events go out later in order.
func TestRateLimitIsPersisted(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := open(t, dir)
	var s sink
	srv := s.server(t)
	tg := []config.NotifyConfig{target(2)}
	urls := map[string]string{"hook": srv.URL}
	enqueue(t, st, tg, findings.Result{Opened: []domain.Finding{finding(1, domain.SeverityError), finding(2, domain.SeverityError), finding(3, domain.SeverityError)}}, t0)
	if rep, _ := Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, t0); rep.Sent != 2 || rep.Held != 1 {
		t.Fatalf("first pass %+v", rep)
	}
	st.Close()
	st = open(t, dir)
	defer st.Close()
	if rep, _ := Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, t0.Add(30*time.Minute)); rep.Sent != 0 || rep.Held != 1 {
		t.Fatalf("after restart %+v", rep)
	}
	if rep, _ := Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, t0.Add(61*time.Minute)); rep.Sent != 1 {
		t.Fatalf("an hour later %+v", rep)
	}
}

// Failures back off and give up; errors never quote the URL (tokens).
func TestFailuresBackOffAndDrop(t *testing.T) {
	ctx := context.Background()
	st := open(t, t.TempDir())
	defer st.Close()
	s := sink{status: 500}
	srv := s.server(t)
	tg := []config.NotifyConfig{target(100)}
	urls := map[string]string{"hook": srv.URL + "/tok-SECRET"}
	enqueue(t, st, tg, findings.Result{Opened: []domain.Finding{finding(1, domain.SeverityError)}}, t0)
	now := t0
	settings := config.Defaults().Notifications
	for i := 0; i < settings.MaxAttempts; i++ {
		rep, err := Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, now)
		if err != nil || rep.Failed != 1 {
			t.Fatalf("attempt %d: %+v %v", i, rep, err)
		}
		// Not retried before its backoff.
		if rep, _ := Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, now.Add(30*time.Second)); rep.Failed != 0 {
			t.Fatalf("retried before backoff: %+v", rep)
		}
		now = now.Add(settings.MaxBackoff.Duration)
	}
	ns, _ := st.RecentNotifications(ctx, 5)
	if ns[0].DroppedAt == nil || strings.Contains(ns[0].LastError, "SECRET") || !strings.Contains(ns[0].LastError, "HTTP 500") {
		t.Fatalf("%+v", ns[0])
	}
	// An unreachable URL: the transport error is not echoed.
	enqueue(t, st, tg, findings.Result{Opened: []domain.Finding{finding(2, domain.SeverityError)}}, now)
	_, _ = Deliver(ctx, st, tg, config.Defaults().Notifications, map[string]string{"hook": "http://127.0.0.1:1/tok-SECRET"}, nil, now)
	ns, _ = st.RecentNotifications(ctx, 1)
	if strings.Contains(ns[0].LastError, "SECRET") || ns[0].LastError == "" {
		t.Fatalf("%q", ns[0].LastError)
	}
}

func TestTextFormatAndExpiry(t *testing.T) {
	ctx := context.Background()
	st := open(t, t.TempDir())
	defer st.Close()
	var s sink
	srv := s.server(t)
	tg := []config.NotifyConfig{{ID: "hook", Format: "text", MinSeverity: "notice", MaxPerHour: 10}}
	urls := map[string]string{"hook": srv.URL}
	f := finding(1, domain.SeverityError)
	f.Title = "Bad\ntitle"
	enqueue(t, st, tg, findings.Result{Opened: []domain.Finding{f}}, t0)
	if rep, _ := Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, t0); rep.Sent != 1 {
		t.Fatal(rep)
	}
	r := s.got[0]
	if r.Header.Get("Title") != "Bad title" || r.Header.Get("Priority") != "high" || !strings.HasPrefix(s.bodies[0], "No verified progress") {
		t.Fatalf("headers %v body %q", r.Header, s.bodies[0])
	}
	// Undelivered for a day (target unconfigured meanwhile): dropped, not sent late.
	enqueue(t, st, tg, findings.Result{Opened: []domain.Finding{finding(2, domain.SeverityError)}}, t0)
	if rep, _ := Deliver(ctx, st, tg, config.Defaults().Notifications, urls, nil, t0.Add(25*time.Hour)); rep.Dropped != 1 || rep.Sent != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestDeliveryDoesNotFollowRedirect(t *testing.T) {
	ctx := context.Background()
	st := open(t, t.TempDir())
	defer st.Close()
	receiver := &sink{}
	redirected := receiver.server(t)
	original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirected.URL, http.StatusTemporaryRedirect)
	}))
	defer original.Close()
	targets := []config.NotifyConfig{target(6)}
	enqueue(t, st, targets, findings.Result{Opened: []domain.Finding{finding(1, domain.SeverityWarning)}}, t0)
	rep, err := Deliver(ctx, st, targets, config.Defaults().Notifications, map[string]string{"hook": original.URL}, original.Client(), t0)
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	if err != nil || rep.Failed != 1 || len(receiver.got) != 0 {
		t.Fatalf("delivery escaped configured endpoint: report %+v, redirected requests %d, err %v", rep, len(receiver.got), err)
	}
}

func TestNotificationOutboxRedactsTitles(t *testing.T) {
	ctx := context.Background()
	st := open(t, t.TempDir())
	defer st.Close()
	f := finding(1, domain.SeverityWarning)
	f.Title = "Synthetic release title https://tracker.example/announce?passkey=SECRETVALUE"
	enqueue(t, st, []config.NotifyConfig{target(1)}, findings.Result{Opened: []domain.Finding{f}}, t0)
	pending, err := st.DueNotifications(ctx, "hook", t0, 1)
	if err != nil || len(pending) != 1 {
		t.Fatalf("outbox: %+v, %v", pending, err)
	}
	if strings.Contains(pending[0].Title, "SECRETVALUE") {
		t.Fatalf("notification title retained a tracker credential: %s", pending[0].Title)
	}
}

func TestConfiguredNotificationBoundsControlBatchingAndBackoff(t *testing.T) {
	ctx := context.Background()
	st := open(t, t.TempDir())
	defer st.Close()
	settings := config.Defaults().Notifications
	settings.DigestThreshold = 1
	settings.MaxAttempts = 3
	settings.InitialBackoff = config.Duration{Duration: 2 * time.Minute}
	settings.MaxBackoff = config.Duration{Duration: 3 * time.Minute}
	tg := []config.NotifyConfig{target(10)}
	res := findings.Result{Opened: []domain.Finding{finding(1, domain.SeverityWarning), finding(2, domain.SeverityWarning)}}
	if err := st.Write(ctx, func(tx *store.Tx) error { return Enqueue(tx, tg, settings, res, t0) }); err != nil {
		t.Fatal(err)
	}
	srv := (&sink{status: http.StatusServiceUnavailable}).server(t)
	urls := map[string]string{"hook": srv.URL}
	for _, step := range []struct {
		at, next time.Duration
		dropped  bool
	}{
		{at: 0, next: 2 * time.Minute},
		{at: 2 * time.Minute, next: 5 * time.Minute},
		{at: 5 * time.Minute, dropped: true},
	} {
		if rep, err := Deliver(ctx, st, tg, settings, urls, nil, t0.Add(step.at)); err != nil || rep.Failed != 1 {
			t.Fatalf("delivery at %s: %+v, %v", step.at, rep, err)
		}
		rows, err := st.RecentNotifications(ctx, 2)
		if err != nil || len(rows) != 1 || rows[0].Kind != KindDigest {
			t.Fatalf("expected one digest: %+v, %v", rows, err)
		}
		if (rows[0].DroppedAt != nil) != step.dropped || (!step.dropped && !rows[0].NextAt.Equal(t0.Add(step.next))) {
			t.Fatalf("retry after %s: %+v", step.at, rows[0])
		}
	}
}
