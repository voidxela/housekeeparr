package web

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// The Coverage page renders the shared coverage query: the same totals and
// gap states as the CLI.
func TestCoveragePageMatchesTheQuery(t *testing.T) {
	svc, st := demoServiceStore(t, nil)
	ctx := context.Background()
	now := svc.Now()
	if err := st.Write(ctx, func(tx *store.Tx) error {
		if err := tx.StartCoverageSweep("demo-sonarr", []domain.CoverageGroup{{ID: 1, Title: "Harbor Lights", Monitored: true}}, now); err != nil {
			return err
		}
		aired := now.Add(-48 * time.Hour)
		return tx.SaveCoverageGroup("demo-sonarr", 1, []domain.CoverageItem{
			{Key: "episode:9001", GroupID: 1, Label: "Harbor Lights S02E07", Wanted: true, HasFile: false, AiredAt: &aired},
			{Key: "episode:9002", GroupID: 1, Label: "Harbor Lights S02E08", Wanted: true, HasFile: true},
		}, now)
	}); err != nil {
		t.Fatal(err)
	}
	cv, err := svc.Coverage(ctx, "", 0, 100)
	if err != nil || len(cv.Managers) != 1 {
		t.Fatalf("%+v %v", cv, err)
	}
	s, err := New(svc, Options{Listen: "127.0.0.1:8484", Logger: slog.New(slog.NewTextHandler(testWriter{t}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/coverage", nil)
	r.Host = "127.0.0.1:8484"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "<strong>1 of 2</strong>") || !strings.Contains(body, "Harbor Lights S02E07") ||
		!strings.Contains(body, "Incomplete") {
		t.Fatalf("page %d: %s", w.Code, body)
	}
}
