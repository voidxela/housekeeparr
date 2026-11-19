package arr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// historyServer serves total records for one download, newest first, by
// page; shift inserts that many newer records before page 2 is served.
func historyServer(t *testing.T, total, shift int) (*Client, *[]string) {
	t.Helper()
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		pages = append(pages, q.Get("page")+"/"+q.Get("pageSize"))
		page, _ := strconv.Atoi(q.Get("page"))
		size, _ := strconv.Atoi(q.Get("pageSize"))
		n := total
		if page > 1 {
			n += shift
		}
		var recs []string
		for i := (page - 1) * size; i < page*size && i < n; i++ {
			id := n - i // newest first
			ev := "downloadFolderImported"
			if id == 1 {
				ev = "grabbed"
			}
			recs = append(recs, fmt.Sprintf(`{"id":%d,"eventType":%q,"downloadId":"AB","episodeId":%d,"date":"2026-11-16T19:00:00Z","data":{}}`, id, ev, id))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"page":%d,"pageSize":%d,"totalRecords":%d,"records":[%s]}`, page, size, n, strings.Join(recs, ","))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{App: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	c.HistoryPageSize, c.HistoryMaxPages = 3, 3
	return c, &pages
}

func TestHistoryReadsEveryPage(t *testing.T) {
	c, pages := historyServer(t, 7, 0)
	hs, err := c.History(context.Background(), "ab")
	if err != nil || len(hs) != 7 || hs[0].ID != 7 || hs[6].EventType != "grabbed" {
		t.Fatalf("%+v %v", hs, err)
	}
	if strings.Join(*pages, ",") != "1/3,2/3,3/3" {
		t.Fatalf("pages %v", *pages)
	}
}

// Beyond the page bound the newest records are returned with
// ErrHistoryIncomplete, never as the whole history.
func TestHistoryBeyondTheBoundIsIncomplete(t *testing.T) {
	c, _ := historyServer(t, 12, 0)
	hs, err := c.History(context.Background(), "ab")
	if !errors.Is(err, ErrHistoryIncomplete) || len(hs) != 9 || hs[0].ID != 12 || !strings.Contains(err.Error(), "12 records") {
		t.Fatalf("%d records, %v", len(hs), err)
	}
}

// A record added while paging shifts the pages: it is incomplete, and the
// repeated record is not returned twice.
func TestHistoryChangedWhileReadingIsIncomplete(t *testing.T) {
	c, _ := historyServer(t, 5, 1)
	hs, err := c.History(context.Background(), "ab")
	if !errors.Is(err, ErrHistoryIncomplete) {
		t.Fatalf("%+v %v", hs, err)
	}
	seen := map[int64]bool{}
	for _, h := range hs {
		if seen[h.ID] {
			t.Fatalf("record %d twice", h.ID)
		}
		seen[h.ID] = true
	}
}
