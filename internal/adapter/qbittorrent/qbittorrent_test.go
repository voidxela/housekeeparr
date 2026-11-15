package qbittorrent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

const fakePasskey = "0123456789abcdef0123456789abcdef"

// fakeQBit emulates the qBittorrent WebUI API authentication and read
// endpoints using fixture bodies.
type fakeQBit struct {
	version  string
	webapi   string
	torrents []byte
	user     string
	pass     string
	logins   atomic.Int32
	expireOn atomic.Int32 // reject the session once on this request number
	reqs     atomic.Int32
	writes   atomic.Int32 // non-login POSTs (must stay zero)
}

func (f *fakeQBit) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.logins.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("username") != f.user || r.Form.Get("password") != f.pass {
			_, _ = w.Write([]byte("Fails."))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "sid-" + time.Now().Format("150405.000000"), Path: "/"})
		_, _ = w.Write([]byte("Ok."))
	})
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				f.writes.Add(1)
			}
			n := f.reqs.Add(1)
			if _, err := r.Cookie("SID"); err != nil || n == f.expireOn.Load() {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("Forbidden"))
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/api/v2/app/version", auth(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(f.version)) }))
	mux.HandleFunc("/api/v2/app/webapiVersion", auth(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(f.webapi)) }))
	mux.HandleFunc("/api/v2/torrents/info", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(f.torrents)
	}))
	return mux
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func setup(t *testing.T, f *fakeQBit) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c, err := New(Options{ID: "qbit", BaseURL: srv.URL + "/", Username: f.user, Password: f.pass, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func byName(snap domain.TransferSnapshot) map[string]domain.Transfer {
	out := map[string]domain.Transfer{}
	for _, tr := range snap.Transfers {
		out[tr.Name] = tr
	}
	return out
}

func TestV5ContractNormalization(t *testing.T) {
	f := &fakeQBit{version: "v5.3.0", webapi: "2.11.4", torrents: fixture(t, "v5.3.0-torrents_info.json"), user: "admin", pass: "pw"}
	c := setup(t, f)
	ctx := context.Background()
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.App != "qBittorrent" || p.Version != "v5.3.0" || p.APIVersion != "2.11.4" {
		t.Fatalf("probe: %+v", p)
	}
	if p.Compatibility != adapter.CompatTested {
		t.Fatalf("5.3.0 passed the integration suite; got %s", p.Compatibility)
	}
	if p.Read[adapter.CapPrivacy].Support != adapter.Supported {
		t.Fatal("privacy should be supported on 5.x")
	}
	for capName, st := range p.Write {
		if st.Support != adapter.NotImplemented {
			t.Fatalf("write capability %s must be not_implemented, got %s", capName, st.Support)
		}
	}
	snap, err := c.ListTransfers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Complete || len(snap.Transfers) != 7 {
		t.Fatalf("snapshot complete=%v n=%d", snap.Complete, len(snap.Transfers))
	}
	m := byName(snap)

	stalled := m["Example.Show.S01E01.1080p.WEB.h264-TEST"]
	if stalled.StateClass != domain.StateDownloading || stalled.NativeState != "stalledDL" {
		t.Errorf("stalledDL → %s", stalled.StateClass)
	}
	if v, ok := stalled.VerifiedBytes.Get(); !ok || v != 1<<29 {
		t.Errorf("verified bytes %v", stalled.VerifiedBytes)
	}
	if v, ok := stalled.Availability.Get(); !ok || v != 0.5 {
		t.Errorf("availability %v", stalled.Availability)
	}
	if all, ok := stalled.AllFilesWanted.Get(); !ok || !all {
		t.Errorf("all files wanted %v", stalled.AllFilesWanted)
	}
	if p, ok := stalled.Private.Get(); !ok || p {
		t.Errorf("public torrent privacy %v", stalled.Private)
	}
	if stalled.Hashes.V1 != strings.Repeat("a", 40) || stalled.NativeID != strings.Repeat("a", 40) {
		t.Errorf("hashes %+v id %s", stalled.Hashes, stalled.NativeID)
	}
	if !stalled.AddedAt.IsKnown() {
		t.Error("added time should be known")
	}

	partial := m["Example.Show.S01.1080p.WEB.h264-TEST"]
	if all, ok := partial.AllFilesWanted.Get(); !ok || all {
		t.Errorf("deselected files must give AllFilesWanted=false, got %v", partial.AllFilesWanted)
	}
	if p, ok := partial.Private.Get(); !ok || !p {
		t.Errorf("private torrent %v", partial.Private)
	}
	if partial.Hashes.V2 != strings.Repeat("c", 64) || !partial.Hashes.Matches(strings.Repeat("c", 40)) {
		t.Errorf("v2 hash handling %+v", partial.Hashes)
	}

	meta := m["Example.Show.S02E01.720p.HDTV.x264-TEST"]
	if meta.StateClass != domain.StateMetadata {
		t.Errorf("metaDL → %s", meta.StateClass)
	}
	if meta.WantedBytes.IsKnown() || meta.VerifiedBytes.IsKnown() || meta.Private.IsKnown() || meta.AllFilesWanted.IsKnown() {
		t.Error("sizes, privacy, and file selection must be unknown without metadata")
	}
	if meta.SwarmSeeds.Status != domain.Unknown {
		t.Errorf("-1 swarm count must be unknown, got %v", meta.SwarmSeeds)
	}
	if m["Other.Show.S03E04.1080p.WEB.h264-TEST"].StateClass != domain.StatePaused {
		t.Error("stoppedDL must map to paused")
	}
	if m["Other.Show.S03E04.1080p.WEB.h264-TEST"].Availability.Status != domain.Unknown {
		t.Error("availability -1 must be unknown, not zero")
	}
	if m["Finished.Show.S01E02.1080p.WEB.h264-TEST"].StateClass != domain.StateComplete {
		t.Error("stalledUP must map to complete")
	}
	if m["Broken.Show.S01E03.1080p.WEB.h264-TEST"].StateClass != domain.StateError {
		t.Error("missingFiles must map to error")
	}
	if m["Queued.Show.S01E05.1080p.WEB.h264-TEST"].StateClass != domain.StateQueued {
		t.Error("queuedDL must map to queued")
	}

	// Sensitive fields are never retained.
	out, _ := json.Marshal(snap)
	for _, bad := range []string{fakePasskey, "magnet:", "tracker.example", "/downloads/"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("normalized snapshot leaked %q", bad)
		}
	}
	if f.writes.Load() != 0 {
		t.Fatalf("adapter issued %d non-login writes", f.writes.Load())
	}
}

func TestV4ContractNormalization(t *testing.T) {
	f := &fakeQBit{version: "v4.6.7", webapi: "2.9.3", torrents: fixture(t, "v4.6.7-torrents_info.json"), user: "admin", pass: "pw"}
	c := setup(t, f)
	ctx := context.Background()
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Read[adapter.CapPrivacy].Support != adapter.NotSupported {
		t.Fatalf("privacy should be unsupported on 4.6: %+v", p.Read[adapter.CapPrivacy])
	}
	snap, err := c.ListTransfers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := byName(snap)
	tr := m["Example.Show.S01E01.1080p.WEB.h264-TEST"]
	if tr.Private.Status != domain.Unsupported {
		t.Errorf("privacy must be unsupported (not public) on 4.6: %v", tr.Private)
	}
	if m["Other.Show.S03E04.1080p.WEB.h264-TEST"].StateClass != domain.StatePaused {
		t.Error("pausedDL must map to paused")
	}
	if m["Example.Show.S02E01.720p.HDTV.x264-TEST"].WantedBytes.IsKnown() {
		t.Error("metaDL without has_metadata field must still treat sizes as unknown")
	}
}

func TestLoginRejected(t *testing.T) {
	f := &fakeQBit{version: "v5.3.0", torrents: []byte("[]"), user: "admin", pass: "right"}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c, _ := New(Options{ID: "qbit", BaseURL: srv.URL, Username: "admin", Password: "wrong", Timeout: time.Second})
	_, err := c.Probe(context.Background())
	if adapter.ClassOf(err) != adapter.ErrUnauthorized {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("error leaked password")
	}
}

func TestSessionExpiryReauthenticatesOnce(t *testing.T) {
	f := &fakeQBit{version: "v5.3.0", torrents: []byte("[]"), user: "admin", pass: "pw"}
	c := setup(t, f)
	ctx := context.Background()
	if _, err := c.ListTransfers(ctx); err != nil {
		t.Fatal(err)
	}
	f.expireOn.Store(f.reqs.Load() + 1)
	if _, err := c.ListTransfers(ctx); err != nil {
		t.Fatalf("expired session should re-login once: %v", err)
	}
	if f.logins.Load() != 2 {
		t.Fatalf("logins = %d, want 2", f.logins.Load())
	}
}

func TestNoCredentialsRequiresBypass(t *testing.T) {
	f := &fakeQBit{version: "v5.3.0", torrents: []byte("[]")}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c, _ := New(Options{ID: "qbit", BaseURL: srv.URL, Timeout: time.Second})
	_, err := c.ListTransfers(context.Background())
	if adapter.ClassOf(err) != adapter.ErrUnauthorized || !strings.Contains(err.Error(), "bypass") {
		t.Fatalf("got %v", err)
	}
}

func TestMalformedListingIsNotEmpty(t *testing.T) {
	f := &fakeQBit{version: "v5.3.0", torrents: []byte(`{"not":"an array"}`), user: "a", pass: "b"}
	c := setup(t, f)
	snap, err := c.ListTransfers(context.Background())
	if adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("malformed listing must be an error, got %v", err)
	}
	if snap.Complete || len(snap.Transfers) != 0 {
		t.Fatal("a failed read must not produce a complete snapshot")
	}
}

func TestOldVersionUnsupported(t *testing.T) {
	f := &fakeQBit{version: "v4.0.4", torrents: []byte("[]"), user: "a", pass: "b"}
	c := setup(t, f)
	p, err := c.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.Compatibility != adapter.CompatUnsupported {
		t.Fatalf("got %s", p.Compatibility)
	}
}

func TestStateMappingCoversKnownStates(t *testing.T) {
	known := []string{"error", "missingFiles", "uploading", "pausedUP", "stoppedUP", "queuedUP", "stalledUP", "checkingUP", "forcedUP",
		"allocating", "downloading", "metaDL", "forcedMetaDL", "pausedDL", "stoppedDL", "queuedDL", "stalledDL", "checkingDL",
		"forcedDL", "checkingResumeData", "moving"}
	for _, s := range known {
		if stateClass(s) == domain.StateUnknown {
			t.Errorf("state %s unmapped", s)
		}
	}
	if stateClass("somethingNew") != domain.StateUnknown {
		t.Error("unrecognised states must stay unknown")
	}
}

// Captured responses from real released instances (scripts/it/run.sh
// --capture, sanitized). These pin the adapter to observed behaviour.
func TestCapturedReleasedResponses(t *testing.T) {
	for _, tc := range []struct {
		version, webapi, file string
		privacy               domain.SignalStatus
		stopped               string
	}{
		{"v5.3.0", "2.16.2", "captured-v5.3.0-torrents_info.json", domain.Known, "stoppedDL"},
		{"v4.6.7", "2.9.3", "captured-v4.6.7-torrents_info.json", domain.Unsupported, "pausedDL"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			f := &fakeQBit{version: tc.version, webapi: tc.webapi, torrents: fixture(t, tc.file), user: "admin", pass: "pw"}
			c := setup(t, f)
			p, err := c.Probe(context.Background())
			if err != nil || p.Compatibility != adapter.CompatTested {
				t.Fatalf("%s must be recorded as tested: %v %v", tc.version, p.Compatibility, err)
			}
			snap, err := c.ListTransfers(context.Background())
			if err != nil || len(snap.Transfers) != 7 {
				t.Fatalf("%d %v", len(snap.Transfers), err)
			}
			states := map[domain.StateClass]int{}
			for _, tr := range snap.Transfers {
				states[tr.StateClass]++
				if tr.Private.Status != tc.privacy && tr.StateClass != domain.StateMetadata {
					t.Errorf("%s privacy %v", tr.Name, tr.Private)
				}
				if tr.StateClass == domain.StatePaused && tr.NativeState != tc.stopped {
					t.Errorf("stopped state name %q", tr.NativeState)
				}
				if tr.StateClass != domain.StateDownloading && tr.Availability.IsKnown() {
					t.Errorf("%s (%s): availability must be unknown outside active downloading, got %v", tr.Name, tr.NativeState, tr.Availability)
				}
				if v, ok := tr.Availability.Get(); ok && tr.VerifiedBytes.Value > 0 && v != 0.5 {
					t.Errorf("half-complete torrent with no peers: availability %v, observed 0.5 on the real instance", v)
				}
			}
			want := map[domain.StateClass]int{domain.StateDownloading: 4, domain.StatePaused: 1, domain.StateMetadata: 1, domain.StateComplete: 1}
			for k, n := range want {
				if states[k] != n {
					t.Errorf("%s: %d, want %d (%v)", k, states[k], n, states)
				}
			}
		})
	}
}

func TestUnrunVersionsStayUntested(t *testing.T) {
	for _, v := range []string{"v5.2.4", "v5.3.1", "v4.6.6"} {
		f := &fakeQBit{version: v, torrents: []byte("[]"), user: "a", pass: "b"}
		p, err := setup(t, f).Probe(context.Background())
		if err != nil || p.Compatibility != adapter.CompatUntested {
			t.Errorf("%s: %s %v", v, p.Compatibility, err)
		}
	}
}
