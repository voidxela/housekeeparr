package transmission

import (
	"context"
	"encoding/base64"
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

type fakeTR struct {
	session  []byte
	torrents []byte
	user     string
	pass     string
	sid      string
	conflict atomic.Int32
	methods  []string
}

func (f *fakeTR) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" || r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		if f.user != "" && r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(f.user+":"+f.pass)) {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != f.sid {
			f.conflict.Add(1)
			w.Header().Set("X-Transmission-Session-Id", f.sid)
			w.WriteHeader(409)
			return
		}
		var req struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.methods = append(f.methods, req.Method)
		switch req.Method {
		case "session-get":
			_, _ = w.Write(f.session)
		case "torrent-get":
			fs, _ := json.Marshal(req.Arguments["fields"])
			for _, banned := range []string{"trackerStats", "trackers", "magnetLink", "downloadDir"} {
				if strings.Contains(string(fs), `"`+banned+`"`) {
					t.Errorf("adapter requested sensitive field %s", banned)
				}
			}
			_, _ = w.Write(f.torrents)
		default:
			t.Errorf("adapter sent non-read method %q", req.Method)
			_, _ = w.Write([]byte(`{"result":"method name not recognized"}`))
		}
	})
}

func load(t *testing.T, n string) []byte {
	b, err := os.ReadFile("testdata/" + n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func client(t *testing.T, f *fakeTR, user, pass string) *Client {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c, err := New(Options{ID: "tr", BaseURL: srv.URL, Username: user, Password: pass, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestContract406(t *testing.T) {
	f := &fakeTR{session: load(t, "v4.0.6-session-get.json"), torrents: load(t, "v4.0.6-torrent-get.json"), user: "u", pass: "p", sid: "sid-1"}
	c := client(t, f, "u", "p")
	ctx := context.Background()
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.App != "Transmission" || p.Version != "4.0.6 (38c164933e)" || p.Compatibility != adapter.CompatTested || len(p.Limitations) != 2 {
		t.Fatalf("%+v", p)
	}
	if st := p.Write[adapter.CapStopTransfer]; st.Support != adapter.Supported || !strings.Contains(st.Note, "torrent-stop") {
		t.Fatalf("stop capability: %+v", st)
	}
	if p.Write[adapter.CapTransferLabel].Support != adapter.NotImplemented || len(p.Write) != 2 {
		t.Fatalf("write capabilities: %+v", p.Write)
	}
	if f.conflict.Load() != 1 {
		t.Fatalf("expected exactly one 409 handshake, got %d", f.conflict.Load())
	}
	snap, err := c.ListTransfers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Complete || len(snap.Transfers) != 10 {
		t.Fatalf("%d transfers", len(snap.Transfers))
	}
	m := map[string]domain.Transfer{}
	for _, tr := range snap.Transfers {
		m[tr.NativeID[:1]] = tr
	}
	a := m["a"]
	if a.StateClass != domain.StateDownloading || a.VerifiedBytes.Value != 1<<29 || !a.AvailabilityWantedOnly {
		t.Errorf("a: %+v", a)
	}
	if v, ok := a.Availability.Get(); !ok || v < 0.33 || v > 0.34 {
		t.Errorf("a availability %v (want desiredAvailable/leftUntilDone ≈ 0.333)", a.Availability)
	}
	b := m["b"]
	if all, _ := b.AllFilesWanted.Get(); all || !b.Private.Value || b.Availability.Value != 1 {
		t.Errorf("b: allWanted=%v private=%v avail=%v", b.AllFilesWanted, b.Private, b.Availability)
	}
	c3 := m["c"]
	if c3.StateClass != domain.StateMetadata || c3.WantedBytes.IsKnown() || c3.Private.IsKnown() {
		t.Errorf("magnet: %+v", c3)
	}
	for k, want := range map[string]domain.StateClass{"d": domain.StatePaused, "e": domain.StateComplete, "f": domain.StateError,
		"1": domain.StateQueued, "2": domain.StateDownloading, "3": domain.StateComplete, "4": domain.StateChecking} {
		if m[k].StateClass != want {
			t.Errorf("%s: %s, want %s", k, m[k].StateClass, want)
		}
	}
	if m["d"].Availability.IsKnown() {
		t.Error("availability must be unknown for stopped torrents")
	}
	if m["2"].StateClass == domain.StateError {
		t.Error("tracker errors must not be classed as local transfer errors")
	}
	out, _ := json.Marshal(snap)
	for _, bad := range []string{"0123456789abcdef0123456789abcdef", "magnet:", "/downloads/"} {
		if strings.Contains(string(out), bad) {
			t.Errorf("snapshot leaked %q", bad)
		}
	}
	for _, mth := range f.methods {
		if !readMethods[mth] {
			t.Fatalf("non-read method sent: %s", mth)
		}
	}
}

func TestDeprecationNoteOn41(t *testing.T) {
	f := &fakeTR{session: load(t, "v4.1.4-session-get.json"), torrents: []byte(`{"result":"success","arguments":{"torrents":[]}}`), sid: "x"}
	p, err := client(t, f, "", "").Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Limitations, " "), "deprecated") {
		t.Fatalf("4.1 must note the legacy protocol deprecation: %v", p.Limitations)
	}
}

func TestAuthAndErrors(t *testing.T) {
	f := &fakeTR{session: load(t, "v4.0.6-session-get.json"), user: "u", pass: "p", sid: "s"}
	if _, err := client(t, f, "u", "wrong").Probe(context.Background()); adapter.ClassOf(err) != adapter.ErrUnauthorized {
		t.Fatalf("got %v", err)
	}
	f2 := &fakeTR{session: []byte(`{"result":"success","arguments":{"version":"2.52","rpc-version":14,"rpc-version-semver":"5.0.0"}}`), sid: "s"}
	p, err := client(t, f2, "", "").Probe(context.Background())
	if err != nil || p.Compatibility != adapter.CompatUnsupported {
		t.Fatalf("%v %v", p.Compatibility, err)
	}
	f3 := &fakeTR{session: []byte(`{"result":"error: something"}`), sid: "s"}
	if _, err := client(t, f3, "", "").Probe(context.Background()); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("non-success result: %v", err)
	}
	c := client(t, f, "u", "p")
	if err := c.call(context.Background(), "torrent-remove", nil, nil); adapter.ClassOf(err) != adapter.ErrUnsupported {
		t.Fatalf("write method must be refused locally: %v", err)
	}
}
