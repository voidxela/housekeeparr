package radarr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
)

func server(t *testing.T, status string) *Client {
	t.Helper()
	read := func(n string) []byte {
		b, err := os.ReadFile("testdata/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	st := read("v6.4.4-system_status.json")
	if status != "" {
		st = []byte(status)
	}
	queue, health := read("v6.4.4-queue.json"), read("v6.4.4-health.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("adapter issued %s", r.Method)
		}
		if r.Header.Get("X-Api-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/system/status":
			_, _ = w.Write(st)
		case "/api/v3/queue":
			if r.URL.Query().Get("includeUnknownMovieItems") != "true" || r.URL.Query().Get("sortKey") != "added" {
				t.Errorf("queue query %s", r.URL.RawQuery)
			}
			_, _ = w.Write(queue)
		case "/api/v3/health":
			_, _ = w.Write(health)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{ID: "radarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRadarrContract(t *testing.T) {
	c := server(t, "")
	ctx := context.Background()
	p, err := c.Probe(ctx)
	if err != nil || p.App != "Radarr" || p.Version != "6.4.4.10685" || p.Compatibility != adapter.CompatUntested {
		t.Fatalf("%+v %v", p, err)
	}
	q, err := c.Queue(ctx)
	if err != nil || !q.Complete || len(q.Items) != 3 {
		t.Fatalf("%+v %v", q, err)
	}
	first := q.Items[0]
	if first.ContentKind != "movie" || first.MovieID != 41 || first.ContentLabel() != "Example Film 1 (2024)" || first.DownloadID != strings.Repeat("1", 40) {
		t.Fatalf("%+v label=%q", first, first.ContentLabel())
	}
	if s, _ := q.Items[1].TrackedState.Get(); s != "importPending" {
		t.Fatalf("state %v", q.Items[1].TrackedState)
	}
	if unknown := q.Items[2]; unknown.MovieID != 0 || unknown.ContentLabel() != unknown.Title {
		t.Fatalf("unknown movie %+v", unknown)
	}
	b, _ := json.Marshal(q)
	if strings.Contains(string(b), "/downloads/") {
		t.Fatal("outputPath retained")
	}
	h, err := c.Health(ctx)
	if err != nil || len(h.Items) != 1 || h.Items[0].Source != "IndexerRssCheck" {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestRadarrVersionAndAppChecks(t *testing.T) {
	if p, err := server(t, `{"appName":"Radarr","version":"4.7.5.7809"}`).Probe(context.Background()); err != nil || p.Compatibility != adapter.CompatUnsupported {
		t.Fatalf("%v %v", p.Compatibility, err)
	}
	if _, err := server(t, `{"appName":"Sonarr","version":"4.0.0"}`).Probe(context.Background()); adapter.ClassOf(err) != adapter.ErrUnsupported {
		t.Fatalf("Sonarr must not be accepted as Radarr: %v", err)
	}
}
