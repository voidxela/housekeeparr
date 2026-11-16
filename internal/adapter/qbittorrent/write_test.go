package qbittorrent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStopUsesTheReleasedEndpointPerVersion: qBittorrent 5.0 renamed
// torrents/pause to torrents/stop (both observed by the integration
// harness on 5.3.0 and 4.6.7).
func TestStopUsesTheReleasedEndpointPerVersion(t *testing.T) {
	for _, c := range []struct{ version, path string }{{"v5.3.0", "/api/v2/torrents/stop"}, {"v4.6.7", "/api/v2/torrents/pause"}} {
		t.Run(c.version, func(t *testing.T) {
			f := &fakeQBit{version: c.version, webapi: "2.9.3", user: "admin", pass: "pw"}
			var got []string
			mux := http.NewServeMux()
			mux.Handle("/", f.handler())
			for _, p := range []string{"/api/v2/torrents/stop", "/api/v2/torrents/pause"} {
				p := p
				mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
					if _, err := r.Cookie("SID"); err != nil || r.Method != http.MethodPost || r.Header.Get("Referer") == "" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					_ = r.ParseForm()
					got = append(got, p+"?"+r.Form.Get("hashes"))
				})
			}
			srv := httptest.NewServer(mux)
			defer srv.Close()
			cl, _ := New(Options{ID: "qbit", BaseURL: srv.URL, Username: "admin", Password: "pw", Timeout: 5 * time.Second})
			if err := cl.Stop(context.Background(), "abcd"); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != c.path+"?abcd" {
				t.Fatalf("sent %v, want %s", got, c.path)
			}
		})
	}
}
