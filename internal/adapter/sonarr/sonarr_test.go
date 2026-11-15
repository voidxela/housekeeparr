package sonarr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

const apiKey = "0123456789abcdef0123456789abcdef"

type fakeSonarr struct {
	status  []byte
	pages   map[string][]byte
	health  []byte
	writes  atomic.Int32
	queries []string
	// mutateTotal changes totalRecords on page 2 to simulate a moving queue.
	mutateTotal bool
}

func (f *fakeSonarr) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			f.writes.Add(1)
		}
		if r.URL.Query().Get("apikey") != "" {
			t.Errorf("api key must not be sent in the query string")
		}
		if r.Header.Get("X-Api-Key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch strings.TrimPrefix(r.URL.Path, "/sonarr") {
		case "/api/v3/system/status":
			_, _ = w.Write(f.status)
		case "/api/v3/health":
			_, _ = w.Write(f.health)
		case "/api/v3/queue":
			f.queries = append(f.queries, r.URL.RawQuery)
			body := f.pages[r.URL.Query().Get("page")]
			if f.mutateTotal && r.URL.Query().Get("page") == "2" {
				body = []byte(strings.Replace(string(body), `"totalRecords": 6`, `"totalRecords": 7`, 1))
			}
			if body == nil {
				body = []byte(`{"page":9,"pageSize":3,"totalRecords":6,"records":[]}`)
			}
			_, _ = w.Write(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFake(t *testing.T) *fakeSonarr {
	return &fakeSonarr{
		status: load(t, "v4.0.20-system_status.json"),
		pages:  map[string][]byte{"1": load(t, "v4.0.20-queue_page1.json"), "2": load(t, "v4.0.20-queue_page2.json")},
		health: load(t, "v4.0.20-health.json"),
	}
}

func client(t *testing.T, f *fakeSonarr, key string, pageSize, maxPages int) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c, err := New(Options{ID: "sonarr", BaseURL: srv.URL + "/sonarr", APIKey: key, Timeout: 5 * time.Second, PageSize: pageSize, MaxPages: maxPages})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProbe(t *testing.T) {
	c := client(t, newFake(t), apiKey, 3, 10)
	p, err := c.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.App != "Sonarr" || p.Version != "4.0.20.3014" || p.Compatibility != adapter.CompatTested {
		t.Fatalf("probe %+v", p)
	}
	for k, v := range p.Write {
		if v.Support != adapter.NotImplemented {
			t.Fatalf("write %s must be not_implemented", k)
		}
	}
}

func TestUnauthorized(t *testing.T) {
	c := client(t, newFake(t), "wrong", 3, 10)
	_, err := c.Probe(context.Background())
	if adapter.ClassOf(err) != adapter.ErrUnauthorized {
		t.Fatalf("got %v", err)
	}
}

func TestQueuePaginationAndNormalization(t *testing.T) {
	f := newFake(t)
	c := client(t, f, apiKey, 3, 10)
	snap, err := c.Queue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Complete || len(snap.Items) != 6 || snap.TotalRecords != 6 {
		t.Fatalf("complete=%v items=%d total=%d %v", snap.Complete, len(snap.Items), snap.TotalRecords, snap.Limitations)
	}
	if len(f.queries) != 2 || !strings.Contains(f.queries[0], "sortKey=added") || !strings.Contains(f.queries[0], "includeUnknownSeriesItems=true") {
		t.Fatalf("queries %v", f.queries)
	}
	byID := map[int64]domain.QueueItem{}
	for _, it := range snap.Items {
		byID[it.QueueID] = it
	}
	first := byID[1001]
	if first.DownloadID != strings.Repeat("a", 40) {
		t.Errorf("download id must be normalized lowercase: %q", first.DownloadID)
	}
	if first.ContentLabel() != "Example Show S01E01" {
		t.Errorf("label %q", first.ContentLabel())
	}
	if s, ok := first.TrackedState.Get(); !ok || s != "downloading" {
		t.Errorf("tracked state %v", first.TrackedState)
	}
	blocked := byID[1002]
	if s, _ := blocked.TrackedState.Get(); s != "importBlocked" || len(blocked.StatusMessages) != 1 {
		t.Errorf("blocked %+v", blocked)
	}
	unknownSeries := byID[1005]
	if unknownSeries.SeriesID != 0 || unknownSeries.SeasonNumber.IsKnown() {
		t.Errorf("unknown series item %+v", unknownSeries)
	}
	if strings.Contains(unknownSeries.ErrorMessage, "SECRETAPIKEY") {
		t.Errorf("error message not sanitized: %s", unknownSeries.ErrorMessage)
	}
	pending := byID[1006]
	if pending.DownloadID != "" || pending.TrackedState.IsKnown() {
		t.Errorf("pending item %+v", pending)
	}
	out, _ := json.Marshal(snap)
	if strings.Contains(string(out), "/downloads/") {
		t.Error("outputPath must not be retained")
	}
	if f.writes.Load() != 0 {
		t.Fatal("adapter issued writes")
	}
}

func TestQueueTruncationIsIncomplete(t *testing.T) {
	c := client(t, newFake(t), apiKey, 3, 1)
	snap, err := c.Queue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Complete || len(snap.Items) != 3 || len(snap.Limitations) == 0 {
		t.Fatalf("truncated listing must be incomplete: %+v", snap.Limitations)
	}
}

func TestQueueChangingDuringReadIsIncomplete(t *testing.T) {
	f := newFake(t)
	f.mutateTotal = true
	c := client(t, f, apiKey, 3, 10)
	snap, err := c.Queue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Complete {
		t.Fatal("moving queue must produce an incomplete snapshot")
	}
}

func TestHealth(t *testing.T) {
	c := client(t, newFake(t), apiKey, 3, 10)
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Items) != 3 || h.Items[0].Source != "IndexerSearchCheck" || h.Items[1].Type != "error" {
		t.Fatalf("%+v", h.Items)
	}
	if !strings.HasPrefix(h.Items[0].WikiURL, "https://wiki.servarr.com/sonarr/system#") {
		t.Fatalf("wiki url %q", h.Items[0].WikiURL)
	}
}

func TestHTMLResponseIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>login</html>"))
	}))
	defer srv.Close()
	c, _ := New(Options{ID: "s", BaseURL: srv.URL, APIKey: apiKey})
	_, err := c.Queue(context.Background())
	if adapter.ClassOf(err) != adapter.ErrPermanent || !strings.Contains(err.Error(), "base path") {
		t.Fatalf("got %v", err)
	}
}

func TestOldSonarrUnsupported(t *testing.T) {
	f := newFake(t)
	f.status = []byte(`{"appName":"Sonarr","version":"3.0.10.1567"}`)
	c := client(t, f, apiKey, 3, 10)
	p, err := c.Probe(context.Background())
	if err != nil || p.Compatibility != adapter.CompatUnsupported {
		t.Fatalf("%v %v", p.Compatibility, err)
	}
	f.status = []byte(`{"appName":"Radarr","version":"5.0.0"}`)
	if _, err := c.Probe(context.Background()); adapter.ClassOf(err) != adapter.ErrUnsupported {
		t.Fatalf("non-Sonarr app must be rejected: %v", err)
	}
}

func TestUnrunSonarrVersionUntested(t *testing.T) {
	f := newFake(t)
	f.status = []byte(`{"appName":"Sonarr","version":"4.0.19.2979"}`)
	p, err := client(t, f, apiKey, 3, 10).Probe(context.Background())
	if err != nil || p.Compatibility != adapter.CompatUntested {
		t.Fatalf("%s %v", p.Compatibility, err)
	}
}
