package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
)

func TestBasePathJoin(t *testing.T) {
	c, err := New("https://proxy.example/sonarr/", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.URL("/api/v3/queue", nil); got != "https://proxy.example/sonarr/api/v3/queue" {
		t.Fatalf("got %s", got)
	}
}

func TestRejectsCredentialsInBaseURL(t *testing.T) {
	if _, err := New("http://u:p@host:8080", Options{}); err == nil {
		t.Fatal("expected error for credentials in base url")
	}
	if _, err := New("http://host:8080/?apikey=x", Options{}); err == nil {
		t.Fatal("expected error for query in base url")
	}
}

func TestStatusClassification(t *testing.T) {
	cases := []struct {
		status int
		header map[string]string
		want   adapter.ErrorClass
	}{
		{401, nil, adapter.ErrUnauthorized},
		{403, nil, adapter.ErrUnauthorized},
		{302, nil, adapter.ErrUnauthorized},
		{404, nil, adapter.ErrUnsupported},
		{429, map[string]string{"Retry-After": "120"}, adapter.ErrRateLimited},
		{500, nil, adapter.ErrTransient},
		{503, nil, adapter.ErrTransient},
		{400, nil, adapter.ErrPermanent},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range tc.header {
				w.Header().Set(k, v)
			}
			if tc.status == 302 {
				w.Header().Set("Location", "https://login.example/?token=SECRET")
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("apikey=SECRET detail"))
		}))
		c, _ := New(srv.URL, Options{Timeout: 2 * time.Second})
		_, err := c.Do(context.Background(), Request{Op: "test", Method: "GET", Path: "/x"})
		srv.Close()
		if got := adapter.ClassOf(err); got != tc.want {
			t.Errorf("status %d: class %s, want %s", tc.status, got, tc.want)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("status %d: error leaked secret: %v", tc.status, err)
		}
		if tc.status == 429 {
			var ae *adapter.Error
			if e, ok := err.(*adapter.Error); ok {
				ae = e
			}
			if ae == nil || ae.RetryAfter != 120*time.Second {
				t.Errorf("retry-after not honored: %+v", err)
			}
		}
	}
}

func TestResponseSizeBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, Options{MaxResponseBytes: 1024})
	_, err := c.Do(context.Background(), Request{Op: "test", Method: "GET", Path: "/"})
	if adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("expected permanent error for oversized body, got %v", err)
	}
}

func TestTimeoutClassification(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	c, _ := New(srv.URL, Options{Timeout: 100 * time.Millisecond})
	_, err := c.Do(context.Background(), Request{Op: "read", Method: "GET", Path: "/"})
	if adapter.ClassOf(err) != adapter.ErrTransient {
		t.Fatalf("read timeout should be transient, got %v", err)
	}
	_, err = c.Do(context.Background(), Request{Op: "write", Method: "POST", Path: "/", Write: true})
	if adapter.ClassOf(err) != adapter.ErrUncertain {
		t.Fatalf("write timeout must be uncertain_outcome, got %v", err)
	}
}

func TestConnectionRefusedIsTransientWithoutURL(t *testing.T) {
	c, _ := New("http://127.0.0.1:1/base", Options{Timeout: time.Second})
	_, err := c.Do(context.Background(), Request{Op: "read", Method: "GET", Path: "/api", Query: map[string][]string{"apikey": {"SECRET"}}})
	if adapter.ClassOf(err) != adapter.ErrTransient {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("leaked: %v", err)
	}
}

func TestHTTPWriteTimeoutIsAmbiguous(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer server.Close()
	client, err := New(server.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(context.Background(), Request{Op: "write", Method: "POST", Path: "/", Write: true}); adapter.ClassOf(err) != adapter.ErrUncertain {
		t.Fatalf("HTTP 408 could be an interrupted write, got %v", err)
	}
}
