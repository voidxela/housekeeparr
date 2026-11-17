package radarr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

func TestMissingFixtureAndNormalization(t *testing.T) {
	fixture, err := os.ReadFile("testdata/v6.4.4-wanted_missing.json")
	if err != nil {
		t.Fatal(err)
	}
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k" {
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

	c, err := New(Options{ID: "radarr-1", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := c.Missing(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Complete || snap.Total != 4 || snap.ManagerID != "radarr-1" || len(snap.Items) != 3 {
		t.Fatalf("unexpected snapshot: complete=%v total=%d items=%d", snap.Complete, snap.Total, len(snap.Items))
	}
	if gotQuery.Get("sortKey") != "movieMetadata.sortTitle" || gotQuery.Get("sortDirection") != "ascending" || gotQuery.Get("monitored") != "true" {
		t.Fatalf("unexpected query params: %v", gotQuery)
	}

	byID := map[int64]domain.MissingItem{}
	for _, it := range snap.Items {
		byID[it.ContentID] = it
	}
	if _, ok := byID[603]; ok {
		t.Fatal("movie 603 with hasFile=true must be skipped")
	}

	// 601: isAvailable=true, earliest date is inCinemas (2024-03-01)
	m1 := byID[601]
	if m1.ContentKind != "movie" || m1.Title != "Example Film A (2024)" || !m1.Monitored {
		t.Fatalf("movie 601 mismatch: %+v", m1)
	}
	if avail, ok := m1.Available.Get(); !ok || !avail {
		t.Fatalf("movie 601 availability: %+v", m1.Available)
	}
	wantAired1 := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	if m1.AiredAt == nil || !m1.AiredAt.Equal(wantAired1) {
		t.Fatalf("movie 601 AiredAt=%v, want %v", m1.AiredAt, wantAired1)
	}

	// 602: isAvailable=false, earliest date is digitalRelease (2025-10-01 < inCinemas 2025-11-01)
	m2 := byID[602]
	if avail, ok := m2.Available.Get(); !ok || avail {
		t.Fatalf("movie 602 availability: %+v", m2.Available)
	}
	wantAired2 := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	if m2.AiredAt == nil || !m2.AiredAt.Equal(wantAired2) {
		t.Fatalf("movie 602 AiredAt=%v, want %v", m2.AiredAt, wantAired2)
	}

	// 604: no isAvailable and no release dates -> UnknownValue and AiredAt=nil
	m4 := byID[604]
	if m4.Title != "Example Film D (2026)" || m4.AiredAt != nil || m4.Available.IsKnown() {
		t.Fatalf("movie 604 mismatch: %+v", m4)
	}
}

func TestMissingTwoPagesAndTruncation(t *testing.T) {
	pages := map[string]string{
		"1": `{"totalRecords":3,"records":[
			{"id":1,"title":"Movie 1","year":2024,"monitored":true,"hasFile":false,"isAvailable":true},
			{"id":2,"title":"Movie 2","year":2024,"monitored":true,"hasFile":false,"isAvailable":false}
		]}`,
		"2": `{"totalRecords":3,"records":[
			{"id":3,"title":"Movie 3","year":2024,"monitored":true,"hasFile":false,"isAvailable":true}
		]}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(pages[r.URL.Query().Get("page")]))
	}))
	defer srv.Close()

	c, _ := New(Options{ID: "r", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second, PageSize: 2, MaxPages: 5})
	snap, err := c.Missing(context.Background(), 10)
	if err != nil || !snap.Complete || len(snap.Items) != 3 {
		t.Fatalf("two-page read failed: err=%v snap=%+v", err, snap)
	}
	snap, err = c.Missing(context.Background(), 2)
	if err != nil || snap.Complete || len(snap.Items) != 2 {
		t.Fatalf("truncated read failed: err=%v snap=%+v", err, snap)
	}
}

func TestSearchMovies(t *testing.T) {
	var hits atomic.Int32
	var gotKey string
	var gotBody map[string]any
	resp := `{"id":654,"name":"MoviesSearch"}`

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

	c, _ := New(Options{ID: "r", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if _, err := c.SearchMovies(context.Background(), []int64{}); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("empty ids: %v", err)
	}
	if _, err := c.SearchMovies(context.Background(), []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("11 ids: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected 0 requests, got %d", hits.Load())
	}

	id, err := c.SearchMovies(context.Background(), []int64{601, 602})
	if err != nil || id != 654 || gotKey != "k" {
		t.Fatalf("id=%d err=%v key=%q", id, err, gotKey)
	}
	want := map[string]any{"name": "MoviesSearch", "movieIds": []any{float64(601), float64(602)}}
	if !reflect.DeepEqual(gotBody, want) {
		t.Fatalf("body=%v, want %v", gotBody, want)
	}

	resp = `{}`
	if _, err := c.SearchMovies(context.Background(), []int64{601}); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("response without id must error: %v", err)
	}
}
