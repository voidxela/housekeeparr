package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// Synthetic responses exercising envelope validation shared by Sonarr and
// Radarr.

func serve(t *testing.T, pages map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.URL.Path
		if r.URL.Path == "/api/v3/queue" {
			key = "page" + r.URL.Query().Get("page")
		}
		body, ok := pages[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{App: "sonarr", BaseURL: srv.URL, APIKey: "k", Timeout: 5 * time.Second, PageSize: 2, MaxPages: 3})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func norm(raw json.RawMessage) (domain.QueueItem, error) {
	var r Record
	err := json.Unmarshal(raw, &r)
	return r.Common(), err
}

func TestMalformedQueueEnvelopesAreErrors(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `[]`,
		`{"totalRecords":1}`,
		`{"records":[{"id":1}]}`,
		`{"totalRecords":1,"records":null}`,
		`{"totalRecords":-1,"records":[]}`,
		`{"totalRecords":1,"records":[{"title":"no id"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			snap, err := serve(t, map[string]string{"page1": body}).Queue(context.Background(), "m", nil, norm)
			if err == nil {
				t.Fatalf("accepted: complete=%v items=%d", snap.Complete, len(snap.Items))
			}
		})
	}
}

func TestQueueCountMismatchIsIncomplete(t *testing.T) {
	cases := map[string]map[string]string{
		"short first page":           {"page1": `{"totalRecords":2,"records":[{"id":1}]}`},
		"short later page":           {"page1": `{"totalRecords":5,"records":[{"id":1},{"id":2}]}`, "page2": `{"totalRecords":5,"records":[{"id":3}]}`},
		"more rows than total":       {"page1": `{"totalRecords":1,"records":[{"id":1},{"id":2}]}`, "page2": `{"totalRecords":1,"records":[]}`},
		"empty page but rows remain": {"page1": `{"totalRecords":3,"records":[{"id":1},{"id":2}]}`, "page2": `{"totalRecords":3,"records":[]}`},
		"repeated row":               {"page1": `{"totalRecords":3,"records":[{"id":1},{"id":2}]}`, "page2": `{"totalRecords":3,"records":[{"id":2}]}`},
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := serve(t, pages).Queue(context.Background(), "m", nil, norm)
			if err != nil {
				t.Fatal(err)
			}
			if snap.Complete || len(snap.Limitations) == 0 {
				t.Fatalf("inconsistent listing accepted as complete: %+v", snap)
			}
		})
	}
}

func TestQueueConsistentListingsAreComplete(t *testing.T) {
	cases := map[string]map[string]string{
		"explicit empty": {"page1": `{"totalRecords":0,"records":[]}`},
		"exact pages":    {"page1": `{"totalRecords":4,"records":[{"id":1},{"id":2}]}`, "page2": `{"totalRecords":4,"records":[{"id":3},{"id":4}]}`},
		"short last":     {"page1": `{"totalRecords":3,"records":[{"id":1},{"id":2}]}`, "page2": `{"totalRecords":3,"records":[{"id":3}]}`},
	}
	for name, pages := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := serve(t, pages).Queue(context.Background(), "m", nil, norm)
			if err != nil || !snap.Complete {
				t.Fatalf("%v %+v", err, snap)
			}
		})
	}
}

func TestQueueTruncatedAtPageBound(t *testing.T) {
	p := `{"totalRecords":9,"records":[{"id":%d},{"id":%d}]}`
	pages := map[string]string{}
	for i := 1; i <= 3; i++ {
		pages["page"+string(rune('0'+i))] = sprintf(p, 2*i-1, 2*i)
	}
	snap, err := serve(t, pages).Queue(context.Background(), "m", nil, norm)
	if err != nil || snap.Complete || len(snap.Items) != 6 || len(snap.Limitations) != 1 {
		t.Fatalf("%v %+v", err, snap)
	}
}

func TestHealthRequiresList(t *testing.T) {
	for _, body := range []string{`null`, `{}`} {
		if _, err := serve(t, map[string]string{"/api/v3/health": body}).Health(context.Background(), "m"); err == nil {
			t.Fatalf("%s accepted as health", body)
		}
	}
	snap, err := serve(t, map[string]string{"/api/v3/health": `[]`}).Health(context.Background(), "m")
	if err != nil || len(snap.Items) != 0 {
		t.Fatalf("explicit empty health list: %v %+v", err, snap)
	}
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }
