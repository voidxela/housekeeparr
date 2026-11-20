package web

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/demo"
)

// BenchmarkPageRender renders pages from local state (demo data): the
// layout's per-page cost and the overview's.
func BenchmarkPageRender(b *testing.B) {
	d, err := demo.Generate(context.Background(), b.TempDir(), time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Store.Close()
	svc := app.New(d.Store, d.Config, d.Clock.Now, d.Engine)
	s, err := New(svc, Options{Listen: "127.0.0.1:0", Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		b.Fatal(err)
	}
	h := s.Handler()
	for _, path := range []string{"/", "/attention", "/activity"} {
		b.Run(path, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				r := httptest.NewRequest("GET", path, nil)
				r.Host = "127.0.0.1:8484"
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 {
					b.Fatalf("%s: %d", path, w.Code)
				}
			}
		})
	}
}
