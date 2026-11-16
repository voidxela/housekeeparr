package arr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
)

func TestRemoveQueueItemSendsEveryFlagExplicitly(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c, _ := New(Options{App: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err := c.RemoveQueueItem(context.Background(), 42, RemoveFlags{Blocklist: true}); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodDelete || got.URL.Path != "/api/v3/queue/42" || got.Header.Get("X-Api-Key") != "k" {
		t.Fatalf("request %s %s", got.Method, got.URL)
	}
	q := got.URL.Query()
	want := map[string]string{"removeFromClient": "false", "blocklist": "true", "skipRedownload": "false", "changeCategory": "false"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s=%q, want %q (query %s)", k, q.Get(k), v, got.URL.RawQuery)
		}
	}
	if strings.Contains(got.URL.RawQuery, "apikey") {
		t.Error("API key in query string")
	}
}

func TestRemoveQueueItemTimeoutIsUncertain(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	c, _ := New(Options{App: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 200 * time.Millisecond})
	err := c.RemoveQueueItem(context.Background(), 1, RemoveFlags{})
	var ae *adapter.Error
	if !errors.As(err, &ae) || ae.Class != adapter.ErrUncertain {
		t.Fatalf("timeout on a write must be uncertain: %v", err)
	}
}

func TestBlocklistAndHistoryReads(t *testing.T) {
	// Shape of released BlocklistResource: no infohash.
	c := serve(t, map[string]string{
		"/api/v3/blocklist": `{"totalRecords":3,"records":[
			{"id":3,"sourceTitle":"Show.S01E02.1080p-GRP","date":"2026-11-15T12:00:05Z","seriesId":3,"episodeIds":[9]},
			{"id":2,"sourceTitle":"Show.S01E02.1080p-GRP","date":"2026-11-14T12:00:00Z","seriesId":3,"episodeIds":[9]},
			{"id":1,"sourceTitle":"Film.2020.1080p-GRP","date":"2026-11-15T12:00:05Z","movieId":4}]}`,
		"/api/v3/history": `{"totalRecords":3,"records":[{"id":7,"eventType":"downloadFailed","downloadId":"BBBB"},{"id":6,"eventType":"grabbed","downloadId":"BBBB"},{"id":5,"eventType":"grabbed","downloadId":"CCCC"}]}`,
	})
	ctx := context.Background()
	since := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	for _, c2 := range []struct {
		m    BlocklistMatch
		want bool
	}{
		{BlocklistMatch{SourceTitle: "Show.S01E02.1080p-GRP", Since: since, SeriesID: 3, EpisodeID: 9}, true},
		// Byte-exact: a title differing only in case is another release.
		{BlocklistMatch{SourceTitle: "show.s01e02.1080p-grp", Since: since, SeriesID: 3, EpisodeID: 9}, false},
		{BlocklistMatch{SourceTitle: "Show.S01E02.1080p-GRP", Since: since, SeriesID: 3, EpisodeID: 8}, false},
		{BlocklistMatch{SourceTitle: "Show.S01E02.1080p-GRP", Since: since.Add(time.Hour)}, false},
		{BlocklistMatch{SourceTitle: "Film.2020.1080p-GRP", Since: since, MovieID: 4}, true},
		{BlocklistMatch{SourceTitle: "Film.2020.1080p-GRP", Since: since, MovieID: 5}, false},
	} {
		if ok, err := c.Blocklisted(ctx, c2.m); err != nil || ok != c2.want {
			t.Errorf("%+v: %v %v", c2.m, ok, err)
		}
	}
	if ids, err := c.BlocklistEntries(ctx, BlocklistMatch{SourceTitle: "Show.S01E02.1080p-GRP", SeriesID: 3, EpisodeID: 9}); err != nil || len(ids) != 2 {
		t.Fatalf("entries without Since %v %v", ids, err)
	}
	ev, err := c.History(ctx, "bbbb")
	if err != nil || len(ev) != 2 || ev[0].EventType != "downloadFailed" || ev[0].ID != 7 {
		t.Fatalf("history %v %v (other download ids must be filtered out)", ev, err)
	}
}

func TestRecordsWithoutIDsAreMalformed(t *testing.T) {
	c := serve(t, map[string]string{
		"/api/v3/blocklist": `{"totalRecords":1,"records":[{"sourceTitle":"x","date":"2026-11-15T12:00:05Z"}]}`,
		"/api/v3/history":   `{"records":[{"eventType":"grabbed","downloadId":"X"}]}`,
	})
	if _, err := c.BlocklistEntries(context.Background(), BlocklistMatch{SourceTitle: "x"}); err == nil {
		t.Fatal("blocklist record without id accepted")
	}
	if _, err := c.History(context.Background(), "x"); err == nil {
		t.Fatal("history record without id accepted")
	}
}

func TestMalformedBlocklistIsAnError(t *testing.T) {
	c := serve(t, map[string]string{"/api/v3/blocklist": `{}`, "/api/v3/history": `{}`})
	if _, err := c.Blocklisted(context.Background(), BlocklistMatch{SourceTitle: "x"}); err == nil {
		t.Fatal("malformed blocklist accepted as 'not blocklisted'")
	}
	if _, err := c.History(context.Background(), "x"); err == nil {
		t.Fatal("malformed history accepted as empty")
	}
}
