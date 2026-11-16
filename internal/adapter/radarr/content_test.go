package radarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestContentReadsMovieMonitoring(t *testing.T) {
	movie := `{"id":4,"monitored":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v3/movie/4" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"NotFound"}`))
			return
		}
		_, _ = w.Write([]byte(movie))
	}))
	defer srv.Close()
	c, _ := New(Options{ID: "radarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	ctx := context.Background()
	if cs, err := c.Content(ctx, "movie:4"); err != nil || !cs.Exists || !cs.Monitored {
		t.Fatalf("%+v %v", cs, err)
	}
	movie = `{"id":4,"monitored":false}`
	if cs, err := c.Content(ctx, "movie:4"); err != nil || !cs.Exists || cs.Monitored {
		t.Fatalf("unmonitored: %+v %v", cs, err)
	}
	if cs, err := c.Content(ctx, "movie:5"); err != nil || cs.Exists {
		t.Fatalf("deleted: %+v %v", cs, err)
	}
	if _, err := c.Content(ctx, "episode:4"); err == nil {
		t.Fatal("accepted an episode key")
	}
}
