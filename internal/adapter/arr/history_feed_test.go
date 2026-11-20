package arr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The manager-wide feed keeps only the scorecard's fields: indexer and
// release group names (a URL-like name is redacted), never download or
// info URLs; it pages by id, newest first, and refuses malformed pages.
func TestHistoryFeedKeepsOnlySanitizedFields(t *testing.T) {
	var query string
	body := `{"page":1,"pageSize":2,"totalRecords":3,"records":[
	 {"id":3,"eventType":"grabbed","downloadId":"abc","episodeId":7,"sourceTitle":"Show.S01E07","date":"2026-11-18T10:00:00Z",
	  "data":{"indexer":"Example (Prowlarr)","releaseGroup":"GRP","releaseSource":"Search","downloadUrl":"https://tracker.example/dl?passkey=SECRET",
	   "nzbInfoUrl":"https://tracker.example/t/1"}},
	 {"id":2,"eventType":"grabbed","downloadId":"def","movieId":4,"date":"2026-11-18T09:00:00Z",
	  "data":{"indexer":"https://tracker.example/api?apikey=SECRET"}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	c, err := New(Options{App: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pg, err := c.HistoryFeed(context.Background(), 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "sortKey=id") || !strings.Contains(query, "sortDirection=descending") || strings.Contains(query, "downloadId") {
		t.Fatalf("query %s", query)
	}
	if pg.Total != 3 || len(pg.Records) != 2 {
		t.Fatalf("page %+v", pg)
	}
	r := pg.Records[0]
	if r.ID != 3 || r.DownloadID != "ABC" || r.EpisodeID != 7 || r.Indexer != "Example (Prowlarr)" || r.ReleaseGroup != "GRP" || r.ReleaseSource != "Search" {
		t.Fatalf("record %+v", r)
	}
	if got := fmt.Sprintf("%+v", pg); strings.Contains(got, "SECRET") || strings.Contains(got, "tracker.example") {
		t.Fatalf("a URL or credential was kept: %s", got)
	}
	if pg.Records[1].Indexer != "(redacted)" || pg.Records[1].MovieID != 4 {
		t.Fatalf("record %+v", pg.Records[1])
	}
	for _, bad := range []string{`{"totalRecords":1}`, `{"records":[]}`, `{"totalRecords":1,"records":[{"eventType":"grabbed"}]}`} {
		body = bad
		if _, err := c.HistoryFeed(context.Background(), 1, 2); err == nil {
			t.Errorf("malformed page accepted: %s", bad)
		}
	}
}
