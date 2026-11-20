//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/radarr"
)

// TestSonarrCoverage reads coverage from released Sonarr with the adapter
// and checks it against the episode list the harness reads itself: wanted =
// monitored episode of a monitored series that has aired; have = hasFile.
// Sonarr's own series statistics are logged for comparison, not relied on.
func TestSonarrCoverage(t *testing.T) {
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	sid := ensureSeries(t, a)
	c := sonarrClient(t)
	ctx := context.Background()
	gs, err := c.CoverageGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range gs {
		if g.ID != int64(sid) {
			continue
		}
		found = true
		now := time.Now().UTC()
		items, err := c.CoverageItems(ctx, g, now)
		if err != nil {
			t.Fatal(err)
		}
		var eps []struct {
			ID         int64   `json:"id"`
			Monitored  bool    `json:"monitored"`
			HasFile    bool    `json:"hasFile"`
			AirDateUtc *string `json:"airDateUtc"`
		}
		a.mustJSON(http.MethodGet, "/api/v3/episode", map[string][]string{"seriesId": {fmt.Sprint(sid)}}, nil, &eps)
		wantN, haveN := 0, 0
		for _, e := range eps {
			if !g.Monitored || !e.Monitored || e.AirDateUtc == nil {
				continue
			}
			if at, err := time.Parse(time.RFC3339, *e.AirDateUtc); err == nil && !at.After(now) {
				wantN++
				if e.HasFile {
					haveN++
				}
			}
		}
		gotWant, gotHave := 0, 0
		for _, it := range items {
			if it.Wanted {
				gotWant++
				if it.HasFile {
					gotHave++
				}
			}
		}
		if len(items) != len(eps) || gotWant != wantN || gotHave != haveN || wantN == 0 {
			t.Fatalf("adapter %d items, %d wanted, %d have; episode list %d, %d wanted, %d have", len(items), gotWant, gotHave, len(eps), wantN, haveN)
		}
		var series struct {
			Statistics map[string]any `json:"statistics"`
		}
		a.mustJSON(http.MethodGet, fmt.Sprintf("/api/v3/series/%d", sid), nil, nil, &series)
		t.Logf("series %d: %d episodes, %d wanted (monitored, aired), %d with a file; Sonarr statistics %v", sid, len(eps), wantN, haveN, series.Statistics)
	}
	if !found {
		t.Fatalf("fixture series %d not in the group list (%d groups)", sid, len(gs))
	}
}

// TestRadarrCoverage reads coverage from released Radarr: wanted =
// monitored and isAvailable; have = hasFile.
func TestRadarrCoverage(t *testing.T) {
	mm := managedManifest(t)
	a := arrAPI{t: t, base: env(t, "HK_IT_RADARR_URL"), key: env(t, "HK_IT_RADARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/movies", []int{2000, 2040})
	var movies []struct {
		ID          int64 `json:"id"`
		TmdbID      int   `json:"tmdbId"`
		Monitored   bool  `json:"monitored"`
		IsAvailable bool  `json:"isAvailable"`
		HasFile     bool  `json:"hasFile"`
	}
	a.mustJSON(http.MethodGet, "/api/v3/movie", nil, nil, &movies)
	if len(movies) == 0 {
		addMovie(t, a, mm["film-a"].Managed.TmdbID)
		a.mustJSON(http.MethodGet, "/api/v3/movie", nil, nil, &movies)
	}
	c, err := radarr.New(radarr.Options{ID: "radarr", BaseURL: env(t, "HK_IT_RADARR_URL"), APIKey: env(t, "HK_IT_RADARR_KEY"), Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	gs, _ := c.CoverageGroups(context.Background())
	items, err := c.CoverageItems(context.Background(), gs[0], time.Now())
	if err != nil {
		t.Fatal(err)
	}
	wantN, gotWant := 0, 0
	for _, m := range movies {
		if m.Monitored && m.IsAvailable {
			wantN++
		}
	}
	for _, it := range items {
		if it.Wanted {
			gotWant++
		}
	}
	if len(items) != len(movies) || gotWant != wantN {
		t.Fatalf("adapter %d items, %d wanted; movie list %d, %d wanted", len(items), gotWant, len(movies), wantN)
	}
	t.Logf("%d movies, %d wanted (monitored, available)", len(movies), wantN)
}
