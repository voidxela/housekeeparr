package deluge

import (
	"bytes"
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
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int    `json:"id"`
}

func assertNoDestructivePayload(t *testing.T, body []byte) {
	t.Helper()
	s := strings.ToLower(string(body))
	for _, bad := range []string{"remove_torrent", "remove_data", "erase", "delete"} {
		if strings.Contains(s, bad) {
			t.Fatalf("destructive token %q found in request: %s", bad, string(body))
		}
	}
}

func TestStopSendsPauseTorrentOnce(t *testing.T) {
	const targetID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var pauseCalls []rpcRequest
	var logins atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assertNoDestructivePayload(t, body)
		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req.Method == "auth.login" {
			logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "s", Path: "/"})
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true, "error": nil, "id": req.ID})
			return
		}
		if c, err := r.Cookie("_session_id"); err != nil || c.Value != "s" {
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"message": "Not authenticated", "code": 1}, "id": req.ID})
			return
		}
		pauseCalls = append(pauseCalls, req)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": nil, "id": req.ID})
	}))
	defer srv.Close()

	c, err := New(Options{ID: "deluge", BaseURL: srv.URL, Password: "pw", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background(), targetID); err != nil {
		t.Fatal(err)
	}
	if logins.Load() != 1 {
		t.Fatalf("logins = %d, want 1", logins.Load())
	}
	if len(pauseCalls) != 1 {
		t.Fatalf("pause calls = %d, want 1: %+v", len(pauseCalls), pauseCalls)
	}
	if pauseCalls[0].Method != "core.pause_torrent" {
		t.Fatalf("method = %q, want core.pause_torrent", pauseCalls[0].Method)
	}
	if len(pauseCalls[0].Params) != 1 {
		t.Fatalf("params = %+v, want 1-element list [[id]]", pauseCalls[0].Params)
	}
	ids, ok := pauseCalls[0].Params[0].([]any)
	if !ok || len(ids) != 1 || ids[0] != targetID {
		t.Fatalf("params[0] = %#v, want [%s]", pauseCalls[0].Params[0], targetID)
	}

	// Destructive methods must be refused locally before any HTTP call.
	for _, bad := range []string{"core.remove_torrent", "core.remove_torrents", "core.resume_torrent"} {
		if _, err := c.rawCall(context.Background(), bad, []any{targetID}, nil); adapter.ClassOf(err) != adapter.ErrUnsupported {
			t.Fatalf("%s: got %v, want ErrUnsupported", bad, err)
		}
	}
	if len(pauseCalls) != 1 {
		t.Fatalf("rejected methods hit the server: %d calls", len(pauseCalls))
	}
}

func TestStopTimeoutIsUncertain(t *testing.T) {
	block := make(chan struct{})

	var writeHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assertNoDestructivePayload(t, body)
		var req rpcRequest
		_ = json.Unmarshal(body, &req)
		if req.Method == "auth.login" {
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "s", Path: "/"})
			_ = json.NewEncoder(w).Encode(map[string]any{"result": true, "error": nil, "id": req.ID})
			return
		}
		writeHits.Add(1)
		<-block
	}))
	defer srv.Close()
	defer close(block)

	c, err := New(Options{ID: "deluge", BaseURL: srv.URL, Password: "pw", Timeout: 200 * time.Millisecond})
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

func TestStopSessionExpiryRetry(t *testing.T) {
	t.Run("Not authenticated (code 1) re-logs in and resends once", func(t *testing.T) {
		f := &fakeWeb{password: "pw", connected: true, status: fixture(t)}
		baseHandler := f.handler(t)
		var pauseHits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			assertNoDestructivePayload(t, body)
			var req rpcRequest
			_ = json.Unmarshal(body, &req)
			if req.Method == "core.pause_torrent" {
				if c, err := r.Cookie("_session_id"); err != nil || c.Value != "s" || f.expire.Swap(false) {
					pauseHits.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"message": "Not authenticated", "code": 1}, "id": req.ID})
					return
				}
				pauseHits.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": nil, "id": req.ID})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			baseHandler.ServeHTTP(w, r)
		}))
		defer srv.Close()

		c, _ := New(Options{ID: "deluge", BaseURL: srv.URL, Password: "pw", Timeout: 5 * time.Second})
		if _, err := c.Probe(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.expire.Store(true)
		beforeLogins := f.logins.Load()
		if err := c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
			t.Fatal(err)
		}
		if f.logins.Load() != beforeLogins+1 {
			t.Fatalf("logins = %d, want %d", f.logins.Load(), beforeLogins+1)
		}
		if pauseHits.Load() != 2 {
			t.Fatalf("pauseHits = %d, want 2", pauseHits.Load())
		}
	})

	t.Run("repeated code 1 stops after one retry", func(t *testing.T) {
		var pauseHits, logins atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var req rpcRequest
			_ = json.Unmarshal(body, &req)
			if req.Method == "auth.login" {
				logins.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"result": true, "error": nil, "id": req.ID})
				return
			}
			pauseHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"message": "Not authenticated", "code": 1}, "id": req.ID})
		}))
		defer srv.Close()

		c, _ := New(Options{ID: "deluge", BaseURL: srv.URL, Password: "pw", Timeout: 5 * time.Second})
		err := c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		if adapter.ClassOf(err) != adapter.ErrUnauthorized {
			t.Fatalf("got %v, want ErrUnauthorized", err)
		}
		if pauseHits.Load() != 2 || logins.Load() != 2 {
			t.Fatalf("pauseHits=%d logins=%d, want 2 and 2", pauseHits.Load(), logins.Load())
		}
	})

	t.Run("other RPC errors and HTTP errors are never retried", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			status int
			code   int
		}{
			{name: "rpc code 2", status: http.StatusOK, code: 2},
			{name: "http 500", status: http.StatusInternalServerError},
			{name: "http 403", status: http.StatusForbidden},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var pauseHits, logins atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					var req rpcRequest
					_ = json.Unmarshal(body, &req)
					if req.Method == "auth.login" {
						logins.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]any{"result": true, "error": nil, "id": req.ID})
						return
					}
					pauseHits.Add(1)
					if tc.status != http.StatusOK {
						w.WriteHeader(tc.status)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": map[string]any{"message": "other error", "code": tc.code}, "id": req.ID})
				}))
				defer srv.Close()

				c, _ := New(Options{ID: "deluge", BaseURL: srv.URL, Password: "pw", Timeout: 5 * time.Second})
				if err := c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
					t.Fatal("expected error")
				}
				if pauseHits.Load() != 1 || logins.Load() != 1 {
					t.Fatalf("pauseHits=%d logins=%d, want 1 and 1", pauseHits.Load(), logins.Load())
				}
			})
		}
	})
}
