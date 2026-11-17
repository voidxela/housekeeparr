package sonarr

import (
	"context"
	"encoding/json"
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

func TestMissingFixtureAndNormalization(t *testing.T) {
	fixture := load(t, "v4.0.20-wanted_missing.json")
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/v3/wanted/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	c, err := New(Options{ID: "sonarr-1", BaseURL: srv.URL, APIKey: apiKey, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Missing(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Complete || snap.Total != 5 || snap.ManagerID != "sonarr-1" || len(snap.Items) != 4 {
		t.Fatalf("unexpected snapshot: complete=%v total=%d items=%d", snap.Complete, snap.Total, len(snap.Items))
	}
	if gotQuery.Get("sortKey") != "episodes.airDateUtc" || gotQuery.Get("sortDirection") != "descending" ||
		gotQuery.Get("monitored") != "true" || gotQuery.Get("includeSeries") != "true" {
		t.Fatalf("unexpected query params: %v", gotQuery)
	}

	byID := map[int64]domain.MissingItem{}
	for _, it := range snap.Items {
		byID[it.ContentID] = it
	}
	if _, ok := byID[503]; ok {
		t.Fatal("record 503 with hasFile=true must be skipped")
	}

	// 501: future airDateUtc -> KnownValue(false)
	future := byID[501]
	if future.ContentKind != "episode" || future.SeriesID != 12 || future.Season != 1 || future.Episode != 5 ||
		future.Title != "Example Show S01E05" || !future.Monitored || future.AiredAt == nil {
		t.Fatalf("future item mismatch: %+v", future)
	}
	if avail, ok := future.Available.Get(); !ok || avail {
		t.Fatalf("future episode must be KnownValue(false), got %+v", future.Available)
	}

	// 502: past airDateUtc -> KnownValue(true)
	aired := byID[502]
	if avail, ok := aired.Available.Get(); !ok || !avail {
		t.Fatalf("aired episode must be KnownValue(true), got %+v", aired.Available)
	}

	// 504: missing series & null airDateUtc -> fallback title & UnknownValue
	undated := byID[504]
	if undated.Title != "Undated Standalone Title" || undated.AiredAt != nil || undated.Available.IsKnown() {
		t.Fatalf("undated item mismatch: %+v", undated)
	}
}

func TestMissingTwoPagesTruncationAndValidation(t *testing.T) {
	pages := map[string]string{
		"1": `{"totalRecords":3,"records":[
			{"id":11,"seriesId":2,"seasonNumber":1,"episodeNumber":1,"title":"Ep1","airDateUtc":"2024-01-01T00:00:00Z","monitored":true,"hasFile":false,"series":{"title":"Show"}},
			{"id":12,"seriesId":2,"seasonNumber":1,"episodeNumber":2,"title":"Ep2","airDateUtc":"2024-01-02T00:00:00Z","monitored":true,"hasFile":false,"series":{"title":"Show"}}
		]}`,
		"2": `{"totalRecords":3,"records":[
			{"id":13,"seriesId":2,"seasonNumber":1,"episodeNumber":3,"title":"Ep3","airDateUtc":"2099-01-03T00:00:00Z","monitored":true,"hasFile":false,"series":{"title":"Show"}}
		]}`,
	}
	var mutateTotal bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Query().Get("page")
		body := pages[p]
		if mutateTotal && p == "2" {
			body = `{"totalRecords":4,"records":[{"id":13,"seriesId":2,"seasonNumber":1,"episodeNumber":3,"title":"Ep3","monitored":true,"hasFile":false}]}`
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c, _ := New(Options{ID: "s", BaseURL: srv.URL, APIKey: apiKey, Timeout: 5 * time.Second, PageSize: 2, MaxPages: 5})

	// Normal two-page read.
	snap, err := c.Missing(context.Background(), 10)
	if err != nil || !snap.Complete || len(snap.Items) != 3 {
		t.Fatalf("two-page read failed: err=%v snap=%+v", err, snap)
	}

	// Truncated by maxItems -> Complete=false.
	snap, err = c.Missing(context.Background(), 2)
	if err != nil || snap.Complete || len(snap.Items) != 2 {
		t.Fatalf("truncated read failed: err=%v snap=%+v", err, snap)
	}

	// Total changes between pages -> Complete=false.
	mutateTotal = true
	snap, err = c.Missing(context.Background(), 10)
	if err != nil || snap.Complete {
		t.Fatalf("mutated total must set Complete=false: err=%v snap=%+v", err, snap)
	}
	mutateTotal = false

	// Missing monitored or totalRecords -> malformed.
	pages["1"] = `{"totalRecords":1,"records":[{"id":11,"title":"No monitored"}]}`
	if _, err := c.Missing(context.Background(), 10); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("missing monitored must be malformed, got %v", err)
	}
	pages["1"] = `{"records":[{"id":11,"monitored":true}]}`
	if _, err := c.Missing(context.Background(), 10); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("missing totalRecords must be malformed, got %v", err)
	}
}

func TestSearchEpisodes(t *testing.T) {
	var hits atomic.Int32
	var gotKey string
	var gotBody map[string]any
	resp := `{"id":321,"name":"EpisodeSearch"}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/command" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotKey = r.Header.Get("X-Api-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	c, _ := New(Options{ID: "s", BaseURL: srv.URL, APIKey: apiKey, Timeout: 5 * time.Second})
	if _, err := c.SearchEpisodes(context.Background(), nil); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("empty ids: %v", err)
	}
	if _, err := c.SearchEpisodes(context.Background(), []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("11 ids: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected 0 requests, got %d", hits.Load())
	}

	id, err := c.SearchEpisodes(context.Background(), []int64{501, 502})
	if err != nil || id != 321 || gotKey != apiKey {
		t.Fatalf("id=%d err=%v key=%q", id, err, gotKey)
	}
	want := map[string]any{"name": "EpisodeSearch", "episodeIds": []any{float64(501), float64(502)}}
	if !reflect.DeepEqual(gotBody, want) {
		t.Fatalf("body=%v, want %v", gotBody, want)
	}

	resp = `{"name":"EpisodeSearch"}`
	if _, err := c.SearchEpisodes(context.Background(), []int64{501}); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("response without id must error: %v", err)
	}
}
