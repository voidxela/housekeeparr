package sonarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestContentReadsEpisodeAndSeriesMonitoring(t *testing.T) {
	episode, series := `{"id":9,"seriesId":3,"monitored":true}`, `{"id":3,"monitored":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/episode/9":
			_, _ = w.Write([]byte(episode))
		case "/api/v3/series/3":
			_, _ = w.Write([]byte(series))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"NotFound"}`))
		}
	}))
	defer srv.Close()
	c, _ := New(Options{ID: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	ctx := context.Background()
	got := func(key string) (bool, bool) {
		t.Helper()
		cs, err := c.Content(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		return cs.Exists, cs.Monitored
	}
	if e, m := got("episode:9"); !e || !m {
		t.Fatalf("monitored episode: exists=%v monitored=%v", e, m)
	}
	series = `{"id":3,"monitored":false}`
	if e, m := got("episode:9"); !e || m {
		t.Fatal("an unmonitored series must make the episode effectively unmonitored")
	}
	series, episode = `{"id":3,"monitored":true}`, `{"id":9,"seriesId":3,"monitored":false}`
	if e, m := got("episode:9"); !e || m {
		t.Fatal("unmonitored episode reported monitored")
	}
	if e, _ := got("episode:10"); e {
		t.Fatal("a deleted episode reported as existing")
	}
	episode = `{"id":9,"seriesId":3}`
	if _, err := c.Content(ctx, "episode:9"); err == nil {
		t.Fatal("a response without monitored must be malformed, not unmonitored")
	}
	if _, err := c.Content(ctx, "movie:9"); err == nil {
		t.Fatal("accepted a movie key")
	}
}
