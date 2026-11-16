package rtorrent

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
)

func assertNoDestructivePayload(t *testing.T, body []byte) {
	t.Helper()
	s := strings.ToLower(string(body))
	for _, bad := range []string{"d.close", "d.erase", "d.delete", "remove", "erase"} {
		if strings.Contains(s, bad) {
			t.Fatalf("destructive token %q found in request: %s", bad, string(body))
		}
	}
}

func TestStopSendsDStopOnce(t *testing.T) {
	const targetID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var calls []methodCall

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assertNoDestructivePayload(t, body)
		var mc methodCall
		if err := xml.Unmarshal(body, &mc); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, mc)
		_, _ = w.Write([]byte(`<?xml version="1.0"?><methodResponse><params><param><value><i4>0</i4></value></param></params></methodResponse>`))
	}))
	defer srv.Close()

	c, err := New(Options{ID: "rt", BaseURL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background(), targetID); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].Method != "d.stop" {
		t.Fatalf("method = %q, want d.stop", calls[0].Method)
	}
	if len(calls[0].Params) != 1 || calls[0].Params[0] != targetID {
		t.Fatalf("params = %v, want [%s]", calls[0].Params, targetID)
	}

	// Destructive or unpermitted methods must be rejected locally without hitting the server.
	for _, bad := range []string{"d.close", "d.erase", "d.start", "d.delete_tied"} {
		if _, err := c.call(context.Background(), bad, targetID); adapter.ClassOf(err) != adapter.ErrUnsupported {
			t.Fatalf("%s: got %v, want ErrUnsupported", bad, err)
		}
	}
	if len(calls) != 1 {
		t.Fatalf("rejected methods hit the server: %d calls", len(calls))
	}
}

func TestStopTimeoutIsUncertain(t *testing.T) {
	block := make(chan struct{})

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		assertNoDestructivePayload(t, body)
		hits.Add(1)
		<-block
	}))
	defer srv.Close()
	defer close(block)

	c, err := New(Options{ID: "rt", BaseURL: srv.URL, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	var ae *adapter.Error
	if !errors.As(err, &ae) || ae.Class != adapter.ErrUncertain {
		t.Fatalf("timeout on a write must be uncertain: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("timed-out write must not be retried, got %d hits", hits.Load())
	}
}

func TestStopAuthAndFaultNotRetried(t *testing.T) {
	faultXML, err := os.ReadFile("testdata/fault.xml")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("401 unauthorized", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()

		c, _ := New(Options{ID: "rt", BaseURL: srv.URL, Username: "u", Password: "wrong", Timeout: 5 * time.Second})
		if err := c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); adapter.ClassOf(err) != adapter.ErrUnauthorized {
			t.Fatalf("got %v, want ErrUnauthorized", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("hits = %d, want 1", hits.Load())
		}
	})

	t.Run("xml-rpc fault", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			_, _ = w.Write(faultXML)
		}))
		defer srv.Close()

		c, _ := New(Options{ID: "rt", BaseURL: srv.URL, Timeout: 5 * time.Second})
		if err := c.Stop(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); adapter.ClassOf(err) != adapter.ErrUnsupported {
			t.Fatalf("got %v, want ErrUnsupported", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("hits = %d, want 1", hits.Load())
		}
	})
}
