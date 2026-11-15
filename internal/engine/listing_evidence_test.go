package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter/sonarr"
	"github.com/voidxela/housekeeparr/internal/adapter/transmission"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

// swappable serves a replaceable response body per path.
type swappable struct {
	mu     sync.Mutex
	bodies map[string]string
}

func (s *swappable) set(path, body string) { s.mu.Lock(); s.bodies[path] = body; s.mu.Unlock() }
func (s *swappable) get(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[path]
}

// Synthetic Transmission and Sonarr responses, served to the real adapters.
func realAdapterHarness(t *testing.T) (*harness, *swappable, *swappable) {
	tr := &swappable{bodies: map[string]string{}}
	son := &swappable{bodies: map[string]string{}}
	tr.set("session-get", `{"result":"success","arguments":{"version":"4.0.6 (38c164933e)","rpc-version":17,"rpc-version-semver":"5.3.0"}}`)
	tr.set("torrent-get", torrentList(true))
	son.set("/api/v3/system/status", `{"appName":"Sonarr","version":"4.0.20.3014"}`)
	son.set("/api/v3/health", `[]`)
	son.set("/api/v3/queue", queuePage(1))

	trSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") != "sid" {
			w.Header().Set("X-Transmission-Session-Id", "sid")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req struct {
			Method string `json:"method"`
		}
		_ = jsonDecode(r, &req)
		_, _ = w.Write([]byte(tr.get(req.Method)))
	}))
	t.Cleanup(trSrv.Close)
	sonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(son.get(r.URL.Path)))
	}))
	t.Cleanup(sonSrv.Close)

	h := newHarnessWith(t, baseConfig, func(h *harness) {
		c, err := transmission.New(transmission.Options{ID: "qbit", BaseURL: trSrv.URL, Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		m, err := sonarr.New(sonarr.Options{ID: "sonarr", BaseURL: sonSrv.URL, APIKey: "k", Timeout: 5 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		h.realClient, h.realMgr = c, m
	})
	return h, tr, son
}

func torrentList(present bool) string {
	if !present {
		return `{"result":"success","arguments":{"torrents":[]}}`
	}
	return fmt.Sprintf(`{"result":"success","arguments":{"torrents":[{"hashString":"%s","name":"Example.Show.S01E01","status":4,"error":0,"errorString":"",
"addedDate":%d,"sizeWhenDone":%d,"leftUntilDone":%d,"totalSize":%d,"downloadedEver":%d,"rateDownload":0,"peersConnected":0,
"isPrivate":false,"metadataPercentComplete":1,"desiredAvailable":0}]}}`, hash, t0.Unix(), 10*gib, 8*gib, 10*gib, 2*gib)
}

func queuePage(rows int) string {
	if rows == 0 {
		return `{"page":1,"pageSize":200,"totalRecords":0,"records":[]}`
	}
	return fmt.Sprintf(`{"page":1,"pageSize":200,"totalRecords":1,"records":[{"id":7,"title":"Example.Show.S01E01","status":"downloading",
"trackedDownloadStatus":"ok","trackedDownloadState":"downloading","downloadId":"%s","protocol":"torrent","downloadClient":"qBittorrent",
"seriesId":1,"episodeId":11,"seasonNumber":1,"series":{"title":"Example Show"},"episode":{"seasonNumber":1,"episodeNumber":1,"title":"Pilot"},
"size":%d,"sizeleft":%d}]}`, hash, 10*gib, 8*gib)
}

func (h *harness) transfer() *store.TransferRecord {
	h.t.Helper()
	r, err := h.st.Transfer(ctx, "qbit", hash)
	if err != nil || r == nil {
		h.t.Fatalf("transfer record: %v %v", r, err)
	}
	return r
}

func (h *harness) queueRow() store.QueueRecord {
	h.t.Helper()
	qs, err := h.st.QueueItems(ctx, store.QueueFilter{})
	if err != nil || len(qs) != 1 {
		h.t.Fatalf("queue rows: %d %v", len(qs), err)
	}
	return qs[0]
}

// R1: malformed or inconsistent listings must never act as authoritative
// absence, through the real adapters and the engine.
func TestMalformedListingsCannotRemoveEntitiesOrResolveFindings(t *testing.T) {
	h, tr, son := realAdapterHarness(t)
	h.cycle()
	h.run(120, 10)
	stall := h.mustOneActive(domain.FindingTransferStalled)
	if q := h.queueRow(); !q.Present || q.MatchNativeID != hash {
		t.Fatalf("setup: queue row not correlated: %+v", q)
	}

	torrentCases := []string{
		`{"result":"success","arguments":{}}`,
		`{"result":"success","arguments":{"torrents":null}}`,
		`{"result":"success","arguments":null}`,
	}
	queueCases := []string{
		`{}`, `null`,
		`{"totalRecords":2,"records":[]}`,
		fmt.Sprintf(`{"totalRecords":2,"records":[{"id":9,"downloadId":"%s","protocol":"torrent","title":"other"}]}`, "b"+hash[1:]),
	}
	for _, body := range torrentCases {
		tr.set("torrent-get", body)
		h.run(30, 10)
		if !h.transfer().Present {
			t.Fatalf("transfer marked gone after %s", body)
		}
		if f := h.mustOneActive(domain.FindingTransferStalled); f.ID != stall.ID {
			t.Fatalf("stall finding replaced after %s", body)
		}
		if q := h.queueRow(); !q.Present {
			t.Fatalf("queue row lost after %s", body)
		}
	}
	tr.set("torrent-get", torrentList(true))
	h.run(10, 10)
	for _, body := range queueCases {
		son.set("/api/v3/queue", body)
		h.run(30, 10)
		qs, _ := h.st.QueueItems(ctx, store.QueueFilter{})
		for _, q := range qs {
			if q.DownloadID == hash && !q.Present {
				t.Fatalf("queue row marked gone after %s", body)
			}
		}
		if f := h.mustOneActive(domain.FindingTransferStalled); f.ID != stall.ID {
			t.Fatalf("stall finding changed after queue %s", body)
		}
	}

	// Legitimate explicit empty listings are authoritative.
	son.set("/api/v3/queue", queuePage(0))
	h.run(10, 10)
	qs, _ := h.st.QueueItems(ctx, store.QueueFilter{PresentOnly: true})
	for _, q := range qs {
		if q.DownloadID == hash {
			t.Fatal("explicit empty queue did not remove the row")
		}
	}
	tr.set("torrent-get", torrentList(false))
	h.run(10, 10)
	if h.transfer().Present {
		t.Fatal("explicit empty torrent list did not mark the transfer gone")
	}
	if len(h.active(domain.FindingTransferStalled)) != 0 {
		t.Fatal("stall did not resolve on authoritative disappearance")
	}
}

func jsonDecode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
