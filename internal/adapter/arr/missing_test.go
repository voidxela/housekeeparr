package arr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

func normMissing(raw json.RawMessage) (domain.MissingItem, error) {
	var r struct {
		ID      int64  `json:"id"`
		Title   string `json:"title"`
		HasFile bool   `json:"hasFile"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return domain.MissingItem{}, err
	}
	if r.HasFile {
		return domain.MissingItem{}, nil
	}
	return domain.MissingItem{
		ContentKind: "episode",
		ContentID:   r.ID,
		Title:       r.Title,
		Monitored:   true,
	}, nil
}

func serveMissing(t *testing.T, pages map[string]string, queries *[]url.Values) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/wanted/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if queries != nil {
			*queries = append(*queries, r.URL.Query())
		}
		w.Header().Set("Content-Type", "application/json")
		body, ok := pages["page"+r.URL.Query().Get("page")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{App: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second, PageSize: 2, MaxPages: 3})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMissingTwoPageReadAndHasFileSkipped(t *testing.T) {
	var queries []url.Values
	c := serveMissing(t, map[string]string{
		"page1": `{"totalRecords":3,"records":[{"id":1,"title":"A","hasFile":false},{"id":2,"title":"B","hasFile":true}]}`,
		"page2": `{"totalRecords":3,"records":[{"id":3,"title":"C","hasFile":false}]}`,
	}, &queries)

	snap, err := c.Missing(context.Background(), "mgr1", url.Values{"includeSeries": {"true"}}, 10, normMissing)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Complete || snap.Total != 3 || snap.ManagerID != "mgr1" || len(snap.Items) != 2 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
	if snap.Items[0].ContentID != 1 || snap.Items[0].ManagerID != "mgr1" || snap.Items[1].ContentID != 3 {
		t.Fatalf("unexpected items: %+v", snap.Items)
	}
	if len(queries) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(queries))
	}
	for i, q := range queries {
		if q.Get("monitored") != "true" || q.Get("pageSize") != "2" || q.Get("includeSeries") != "true" {
			t.Fatalf("page %d query %v", i+1, q)
		}
	}
}

func TestMissingMalformedEnvelopes(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `[]`,
		`{"records":[{"id":1}]}`,
		`{"totalRecords":1}`,
		`{"totalRecords":1,"records":null}`,
		`{"totalRecords":-1,"records":[]}`,
		`{"totalRecords":1,"records":[{"title":"no id"}]}`,
		`{"totalRecords":1,"records":[{"id":0,"title":"zero id"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			snap, err := serveMissing(t, map[string]string{"page1": body}, nil).Missing(context.Background(), "m", nil, 0, normMissing)
			if err == nil || adapter.ClassOf(err) != adapter.ErrPermanent {
				t.Fatalf("expected malformed error, got err=%v snap=%+v", err, snap)
			}
		})
	}
}

func TestMissingIncompleteConditions(t *testing.T) {
	t.Run("truncated by maxItems mid-page", func(t *testing.T) {
		var queries []url.Values
		c := serveMissing(t, map[string]string{
			"page1": `{"totalRecords":4,"records":[{"id":1,"title":"A"},{"id":2,"title":"B"}]}`,
			"page2": `{"totalRecords":4,"records":[{"id":3,"title":"C"},{"id":4,"title":"D"}]}`,
		}, &queries)
		snap, err := c.Missing(context.Background(), "m", nil, 1, normMissing)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Complete || len(snap.Items) != 1 || snap.Total != 4 || len(queries) != 1 {
			t.Fatalf("snap=%+v queries=%d", snap, len(queries))
		}
	})

	t.Run("truncated by maxItems at page boundary", func(t *testing.T) {
		var queries []url.Values
		c := serveMissing(t, map[string]string{
			"page1": `{"totalRecords":4,"records":[{"id":1,"title":"A"},{"id":2,"title":"B"}]}`,
			"page2": `{"totalRecords":4,"records":[{"id":3,"title":"C"},{"id":4,"title":"D"}]}`,
		}, &queries)
		snap, err := c.Missing(context.Background(), "m", nil, 2, normMissing)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Complete || len(snap.Items) != 2 || snap.Total != 4 || len(queries) != 1 {
			t.Fatalf("snap=%+v queries=%d", snap, len(queries))
		}
	})

	t.Run("total changes between pages", func(t *testing.T) {
		c := serveMissing(t, map[string]string{
			"page1": `{"totalRecords":3,"records":[{"id":1,"title":"A"},{"id":2,"title":"B"}]}`,
			"page2": `{"totalRecords":4,"records":[{"id":3,"title":"C"}]}`,
		}, nil)
		snap, err := c.Missing(context.Background(), "m", nil, 0, normMissing)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Complete {
			t.Fatalf("expected Complete=false when total changes between pages: %+v", snap)
		}
	})

	t.Run("repeated id across pages", func(t *testing.T) {
		c := serveMissing(t, map[string]string{
			"page1": `{"totalRecords":3,"records":[{"id":1,"title":"A"},{"id":2,"title":"B"}]}`,
			"page2": `{"totalRecords":3,"records":[{"id":2,"title":"B"}]}`,
		}, nil)
		snap, err := c.Missing(context.Background(), "m", nil, 0, normMissing)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Complete || len(snap.Items) != 2 {
			t.Fatalf("expected Complete=false and deduplicated items: %+v", snap)
		}
	})

	t.Run("truncated by MaxPages", func(t *testing.T) {
		c := serveMissing(t, map[string]string{
			"page1": `{"totalRecords":7,"records":[{"id":1},{"id":2}]}`,
			"page2": `{"totalRecords":7,"records":[{"id":3},{"id":4}]}`,
			"page3": `{"totalRecords":7,"records":[{"id":5},{"id":6}]}`,
		}, nil)
		snap, err := c.Missing(context.Background(), "m", nil, 0, normMissing)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Complete || len(snap.Items) != 6 || snap.Total != 7 {
			t.Fatalf("snap=%+v", snap)
		}
	})
}

func TestSearchCommand(t *testing.T) {
	var hits atomic.Int32
	var gotMethod, gotPath, gotAPIKey, gotCT string
	var gotBody []byte
	respBody := `{"id":99,"name":"EpisodeSearch","status":"queued"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("X-Api-Key")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	c, err := New(Options{App: "sonarr", BaseURL: srv.URL, APIKey: "secret-key", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	// Reject 0 or 11 IDs before sending anything.
	for _, ids := range [][]int64{nil, {}, {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, {1, 0}} {
		if _, err := c.Search(context.Background(), "EpisodeSearch", "episodeIds", ids); adapter.ClassOf(err) != adapter.ErrPermanent {
			t.Fatalf("ids=%v: expected ErrPermanent, got %v", ids, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("expected 0 requests for invalid id counts, got %d", hits.Load())
	}

	// Valid search with 10 IDs.
	ten := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cmdID, err := c.Search(context.Background(), "EpisodeSearch", "episodeIds", ten)
	if err != nil {
		t.Fatal(err)
	}
	if cmdID != 99 {
		t.Fatalf("cmdID=%d, want 99", cmdID)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v3/command" || gotAPIKey != "secret-key" || gotCT != "application/json" {
		t.Fatalf("request metadata: method=%s path=%s key=%s ct=%s", gotMethod, gotPath, gotAPIKey, gotCT)
	}
	var decoded map[string]any
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name":       "EpisodeSearch",
		"episodeIds": []any{float64(1), float64(2), float64(3), float64(4), float64(5), float64(6), float64(7), float64(8), float64(9), float64(10)},
	}
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("body=%v, want %v", decoded, want)
	}

	// Response without valid id -> malformed error.
	for _, bad := range []string{`{}`, `{"id":0}`, `{"id":-5}`, `null`, `not-json`} {
		respBody = bad
		if _, err := c.Search(context.Background(), "EpisodeSearch", "episodeIds", []int64{7}); adapter.ClassOf(err) != adapter.ErrPermanent {
			t.Fatalf("response %s: expected ErrPermanent, got %v", bad, err)
		}
	}
}

func TestSearchTimeoutIsUncertain(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)

	c, _ := New(Options{App: "radarr", BaseURL: srv.URL, APIKey: "k", Timeout: 150 * time.Millisecond})
	_, err := c.Search(context.Background(), "MoviesSearch", "movieIds", []int64{42})
	var ae *adapter.Error
	if !errors.As(err, &ae) || ae.Class != adapter.ErrUncertain {
		t.Fatalf("timeout on Search must be ErrUncertain, got %v", err)
	}
}
