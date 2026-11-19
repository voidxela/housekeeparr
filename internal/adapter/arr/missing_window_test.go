package arr

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// missingList serves n missing records honouring page and pageSize.
func missingList(t *testing.T, n int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
		var recs []string
		for i := (page - 1) * size; i < page*size && i < n; i++ {
			recs = append(recs, fmt.Sprintf(`{"id":%d,"title":"E%d"}`, i+1, i+1))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"totalRecords":%d,"records":[%s]}`, n, strings.Join(recs, ","))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{App: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMissingWindowsRotateThroughTheList(t *testing.T) {
	c := missingList(t, 5)
	ctx := context.Background()
	var seen []int64
	start := 1
	for i, want := range []struct {
		n, next int
	}{{2, 2}, {2, 3}, {1, 1}} {
		snap, err := c.MissingFrom(ctx, "m", nil, 2, start, normMissing)
		if err != nil || len(snap.Items) != want.n || snap.NextPage != want.next || snap.Complete || snap.Total != 5 {
			t.Fatalf("window %d from page %d: %+v %v", i, start, snap, err)
		}
		for _, it := range snap.Items {
			seen = append(seen, it.ContentID)
		}
		start = snap.NextPage
	}
	if fmt.Sprint(seen) != "[1 2 3 4 5]" {
		t.Fatalf("windows covered %v", seen)
	}
	// A whole read from the start is complete.
	if snap, err := c.MissingFrom(ctx, "m", nil, 10, 1, normMissing); err != nil || !snap.Complete || snap.NextPage != 1 || len(snap.Items) != 5 {
		t.Fatalf("whole read %+v %v", snap, err)
	}
}

// A cursor beyond a list that shrank restarts from the first page in the
// same read.
func TestMissingWindowPastTheEndRestarts(t *testing.T) {
	c := missingList(t, 3)
	snap, err := c.MissingFrom(context.Background(), "m", nil, 2, 9, normMissing)
	if err != nil || snap.StartPage != 1 || len(snap.Items) != 2 || snap.NextPage != 2 {
		t.Fatalf("%+v %v", snap, err)
	}
}
