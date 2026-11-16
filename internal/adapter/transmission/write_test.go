package transmission

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
)

type rpcRequest struct {
	Method    string         `json:"method"`
	Arguments map[string]any `json:"arguments"`
}

func assertNoDestructivePayload(t *testing.T, body []byte) {
	t.Helper()
	s := strings.ToLower(string(body))
	for _, bad := range []string{"torrent-remove", "delete-local-data", "erase", "close", "remove"} {
		if strings.Contains(s, bad) {
			t.Fatalf("destructive token %q found in request: %s", bad, string(body))
		}
	}
}

func TestStopSendsTorrentStopOnce(t *testing.T) {
	const targetID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var calls []rpcRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assertNoDestructivePayload(t, body)
		if r.Header.Get("X-Transmission-Session-Id") != "sid-1" {
			w.Header().Set("X-Transmission-Session-Id", "sid-1")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, req)
		_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
	}))
	defer srv.Close()

	c, err := New(Options{ID: "tr", BaseURL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background(), targetID); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("sent %d RPC calls after handshake, want 1: %+v", len(calls), calls)
	}
	if calls[0].Method != "torrent-stop" {
		t.Fatalf("method = %q, want torrent-stop", calls[0].Method)
	}
	ids, ok := calls[0].Arguments["ids"].([]any)
	if !ok || len(ids) != 1 || ids[0] != targetID {
		t.Fatalf("arguments = %+v, want ids=[%s]", calls[0].Arguments, targetID)
	}

	// Subsequent Stop reuses session id and sends exactly 1 HTTP request.
	if err := c.Stop(context.Background(), targetID); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("total executed calls = %d, want 2", len(calls))
	}

	// Destructive methods must be rejected locally without sending HTTP requests.
	for _, bad := range []string{"torrent-remove", "torrent-start", "session-close"} {
		if err := c.call(context.Background(), bad, map[string]any{"ids": []string{targetID}}, nil); adapter.ClassOf(err) != adapter.ErrUnsupported {
			t.Fatalf("%s: got %v, want ErrUnsupported", bad, err)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("rejected methods hit the server: %d calls", len(calls))
	}
}

func TestStopTimeoutIsUncertain(t *testing.T) {
	block := make(chan struct{})

	var writeHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assertNoDestructivePayload(t, body)
		if r.Header.Get("X-Transmission-Session-Id") != "sid-1" {
			w.Header().Set("X-Transmission-Session-Id", "sid-1")
			w.WriteHeader(http.StatusConflict)
			return
		}
		writeHits.Add(1)
		<-block
	}))
	defer srv.Close()
	defer close(block)

	c, err := New(Options{ID: "tr", BaseURL: srv.URL, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	var ae *adapter.Error
	if !errors.As(err, &ae) || ae.Class != adapter.ErrUncertain {
		t.Fatalf("timeout on a write must be uncertain: %v", err)
	}
	if writeHits.Load() != 1 {
		t.Fatalf("timed-out write must not be retried, got %d hits", writeHits.Load())
	}
}

func TestStopSessionHandshakeRetry(t *testing.T) {
	t.Run("409 retries at most once and succeeds", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			assertNoDestructivePayload(t, body)
			n := attempts.Add(1)
			if n == 1 {
				w.Header().Set("X-Transmission-Session-Id", "sid-new")
				w.WriteHeader(http.StatusConflict)
				return
			}
			if r.Header.Get("X-Transmission-Session-Id") != "sid-new" {
				t.Fatalf("expected X-Transmission-Session-Id=sid-new, got %q", r.Header.Get("X-Transmission-Session-Id"))
			}
			_, _ = w.Write([]byte(`{"result":"success","arguments":{}}`))
		}))
		defer srv.Close()

		c, _ := New(Options{ID: "tr", BaseURL: srv.URL, Timeout: 5 * time.Second})
		if err := c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
			t.Fatal(err)
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want 2", attempts.Load())
		}
	})

	t.Run("409 twice stops after one retry", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.Header().Set("X-Transmission-Session-Id", "sid-loop")
			w.WriteHeader(http.StatusConflict)
		}))
		defer srv.Close()

		c, _ := New(Options{ID: "tr", BaseURL: srv.URL, Timeout: 5 * time.Second})
		if err := c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
			t.Fatal("expected error after repeated 409")
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want 2 (1 initial + 1 retry)", attempts.Load())
		}
	})

	t.Run("non-409 failures are never retried", func(t *testing.T) {
		for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.WriteHeader(status)
			}))
			c, _ := New(Options{ID: "tr", BaseURL: srv.URL, Timeout: 5 * time.Second})
			if err := c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
				srv.Close()
				t.Fatalf("status %d: expected error", status)
			}
			srv.Close()
			if attempts.Load() != 1 {
				t.Fatalf("status %d: attempts = %d, want 1", status, attempts.Load())
			}
		}
	})
}
