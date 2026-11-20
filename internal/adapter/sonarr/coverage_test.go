package sonarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Only monitored, aired episodes of monitored series are wanted; an
// episode without an air date is not counted.
func TestCoverageCountsMonitoredAiredEpisodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v3/series":
			_, _ = w.Write([]byte(`[{"id":3,"title":"Show","monitored":true},{"id":4,"title":"Old","monitored":false}]`))
		case r.URL.Path == "/api/v3/episode" && r.URL.Query().Get("seriesId") == "3":
			_, _ = w.Write([]byte(`[
				{"id":1,"seasonNumber":1,"episodeNumber":1,"airDateUtc":"2020-01-01T00:00:00Z","monitored":true,"hasFile":true},
				{"id":2,"seasonNumber":1,"episodeNumber":2,"airDateUtc":"2020-01-08T00:00:00Z","monitored":true,"hasFile":false},
				{"id":3,"seasonNumber":1,"episodeNumber":3,"airDateUtc":"2020-01-15T00:00:00Z","monitored":false,"hasFile":false},
				{"id":4,"seasonNumber":1,"episodeNumber":4,"airDateUtc":"2099-01-01T00:00:00Z","monitored":true,"hasFile":false},
				{"id":5,"seasonNumber":1,"episodeNumber":5,"monitored":true,"hasFile":false}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, _ := New(Options{ID: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	ctx := context.Background()
	gs, err := c.CoverageGroups(ctx)
	if err != nil || len(gs) != 2 || !gs[0].Monitored || gs[1].Monitored {
		t.Fatalf("groups %+v %v", gs, err)
	}
	items, err := c.CoverageItems(ctx, gs[0], time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	if err != nil || len(items) != 5 {
		t.Fatalf("items %+v %v", items, err)
	}
	want := map[string][2]bool{"episode:1": {true, true}, "episode:2": {true, false}, "episode:3": {false, false}, "episode:4": {false, false}, "episode:5": {false, false}}
	for _, it := range items {
		if w := want[it.Key]; it.Wanted != w[0] || it.HasFile != w[1] {
			t.Errorf("%s: wanted=%v hasFile=%v, want %v", it.Key, it.Wanted, it.HasFile, w)
		}
	}
	if items[1].Label != "Show S01E02" {
		t.Errorf("label %q", items[1].Label)
	}
	// An unmonitored series wants nothing.
	gs[0].Monitored = false
	items, _ = c.CoverageItems(ctx, gs[0], time.Now())
	for _, it := range items {
		if it.Wanted {
			t.Errorf("%s wanted in an unmonitored series", it.Key)
		}
	}
}
