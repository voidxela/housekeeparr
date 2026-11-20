package radarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A movie is wanted when monitored and available (delayed availability
// keeps it out of the denominator).
func TestCoverageCountsMonitoredAvailableMovies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v3/movie" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":1,"title":"A","year":2020,"monitored":true,"isAvailable":true,"hasFile":true},
			{"id":2,"title":"B","year":2021,"monitored":true,"isAvailable":true,"hasFile":false},
			{"id":3,"title":"C","year":2027,"monitored":true,"isAvailable":false,"hasFile":false},
			{"id":4,"title":"D","year":2019,"monitored":false,"isAvailable":true,"hasFile":false}]`))
	}))
	defer srv.Close()
	c, _ := New(Options{ID: "radarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	gs, _ := c.CoverageGroups(context.Background())
	items, err := c.CoverageItems(context.Background(), gs[0], time.Now())
	if err != nil || len(items) != 4 {
		t.Fatalf("%+v %v", items, err)
	}
	wanted := 0
	for _, it := range items {
		if it.Wanted {
			wanted++
		}
	}
	if wanted != 2 || items[1].Label != "B (2021)" || items[1].Key != "movie:2" {
		t.Fatalf("items %+v", items)
	}
}
