package sonarr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

func serveSources(t *testing.T, indexerBody, statusBody []byte) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
		}
		if r.Header.Get("X-Api-Key") != apiKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/indexer":
			_, _ = w.Write(indexerBody)
		case "/api/v3/indexerstatus":
			_, _ = w.Write(statusBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := New(Options{ID: "sonarr-1", BaseURL: srv.URL, APIKey: apiKey, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSourcesFixtureAndNormalization(t *testing.T) {
	c := serveSources(t, load(t, "v4.0.20-indexer.json"), load(t, "v4.0.20-indexerstatus.json"))
	snap, err := c.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.ManagerID != "sonarr-1" || snap.CollectedAt.IsZero() || len(snap.Sources) != 4 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}

	byID := map[int64]domain.Source{}
	for _, s := range snap.Sources {
		byID[s.ID] = s
	}
	if _, ok := byID[999]; ok {
		t.Fatal("status for unknown indexer 999 must be ignored")
	}

	failing := byID[1]
	if failing.Name != "Example Torrent Failing" || failing.Protocol != "torrent" || !failing.AutomaticSearch {
		t.Fatalf("failing source mismatch: %+v", failing)
	}
	if failing.DisabledUntil == nil || !failing.DisabledUntil.After(time.Now().UTC()) {
		t.Fatalf("expected future DisabledUntil, got %v", failing.DisabledUntil)
	}
	wantFailure := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	if failing.LastFailure == nil || !failing.LastFailure.Equal(wantFailure) {
		t.Fatalf("LastFailure=%v, want %v", failing.LastFailure, wantFailure)
	}

	healthy := byID[2]
	if healthy.DisabledUntil != nil || healthy.LastFailure != nil || !healthy.AutomaticSearch {
		t.Fatalf("healthy source mismatch: %+v", healthy)
	}

	usenet := byID[3]
	if usenet.Protocol != "usenet" || !usenet.AutomaticSearch || usenet.DisabledUntil != nil {
		t.Fatalf("usenet source mismatch: %+v", usenet)
	}

	noAuto := byID[4]
	if noAuto.AutomaticSearch {
		t.Fatalf("expected AutomaticSearch=false, got %+v", noAuto)
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "FAKEPASSKEY1234567890") || strings.Contains(string(raw), "http") {
		t.Fatalf("marshalled snapshot must not contain passkey or URL: %s", raw)
	}
}

func TestSourcesMalformed(t *testing.T) {
	validIndexer := load(t, "v4.0.20-indexer.json")
	validStatus := load(t, "v4.0.20-indexerstatus.json")

	for name, tc := range map[string]struct {
		idx []byte
		st  []byte
	}{
		"null indexer array":       {[]byte("null"), validStatus},
		"null indexerstatus array": {validIndexer, []byte("null")},
		"missing id":               {[]byte(`[{"name":"NoID","protocol":"torrent","enableAutomaticSearch":true}]`), validStatus},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := serveSources(t, tc.idx, tc.st).Sources(context.Background()); err == nil || adapter.ClassOf(err) != adapter.ErrPermanent {
				t.Fatalf("%s: expected ErrPermanent malformed error, got %v", name, err)
			}
		})
	}
}
