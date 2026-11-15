package deluge

import (
	"context"
	"encoding/json"
	"io"
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

type fakeWeb struct {
	password  string
	connected bool
	status    []byte
	logins    atomic.Int32
	expire    atomic.Bool
}

func (f *fakeWeb) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json" || r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
			ID     int    `json:"id"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		var shape map[string]json.RawMessage
		_ = json.Unmarshal(body, &shape)
		if p := strings.TrimSpace(string(shape["params"])); !strings.HasPrefix(p, "[") {
			// Real deluge-web fails with a TypeError on non-list params.
			_ = json.NewEncoder(w).Encode(map[string]any{"result": nil, "id": req.ID,
				"error": map[string]any{"message": "TypeError: argument after * must be an iterable, not NoneType", "code": 4}})
			return
		}
		reply := func(result any, code int) {
			out := map[string]any{"result": result, "error": nil, "id": req.ID}
			if code != 0 {
				out["result"], out["error"] = nil, map[string]any{"message": "Not authenticated", "code": code}
			}
			_ = json.NewEncoder(w).Encode(out)
		}
		if req.Method == "auth.login" {
			f.logins.Add(1)
			if len(req.Params) == 1 && req.Params[0] == f.password {
				http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "s", Path: "/"})
				reply(true, 0)
			} else {
				reply(false, 0)
			}
			return
		}
		if c, err := r.Cookie("_session_id"); err != nil || c.Value != "s" || f.expire.Swap(false) {
			reply(nil, 1)
			return
		}
		switch req.Method {
		case "web.connected":
			reply(f.connected, 0)
		case "daemon.get_version": // exported by deluge/core/daemon.py in 2.0.0, 2.1.1, and 2.2.0
			reply("2.1.1", 0)
		case "core.get_torrents_status":
			keys, _ := json.Marshal(req.Params[1])
			for _, banned := range []string{"trackers", "tracker_status", "tracker_host", "save_path", "download_location", "comment", "files", "peers"} {
				if strings.Contains(string(keys), `"`+banned+`"`) {
					t.Errorf("requested sensitive key %s", banned)
				}
			}
			_, _ = w.Write(f.status)
		default:
			t.Errorf("unexpected method %s", req.Method)
			reply(nil, 2)
		}
	})
}

func newClient(t *testing.T, f *fakeWeb, pw string) *Client {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c, err := New(Options{ID: "deluge", BaseURL: srv.URL, Password: pw, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func fixture(t *testing.T) []byte {
	b, err := os.ReadFile("testdata/v2.1.1-get_torrents_status.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestContract211(t *testing.T) {
	f := &fakeWeb{password: "pw", connected: true, status: fixture(t)}
	c := newClient(t, f, "pw")
	ctx := context.Background()
	p, err := c.Probe(ctx)
	if err != nil || p.App != "Deluge" || p.Version != "2.1.1" || p.Compatibility != adapter.CompatUntested {
		t.Fatalf("%+v %v", p, err)
	}
	snap, err := c.ListTransfers(ctx)
	if err != nil || len(snap.Transfers) != 9 || !snap.Complete {
		t.Fatalf("%d %v", len(snap.Transfers), err)
	}
	m := map[string]domain.Transfer{}
	for _, tr := range snap.Transfers {
		m[tr.NativeID[:1]] = tr
	}
	a := m["a"]
	if a.StateClass != domain.StateDownloading || a.VerifiedBytes.Value != 1<<29 || a.Availability.Value != 0.25 || !a.AllFilesWanted.Value {
		t.Errorf("a: %+v", a)
	}
	if a.SwarmSeeds.Value != 3 {
		t.Errorf("swarm seeds %v", a.SwarmSeeds)
	}
	b := m["b"]
	if b.AllFilesWanted.Value || b.VerifiedBytes.Value != 4<<30 || b.VerifiedBytes.Note == "" || !b.Private.Value {
		t.Errorf("b (deselected; progress-derived): %+v", b)
	}
	cm := m["c"]
	if cm.StateClass != domain.StateMetadata || cm.WantedBytes.IsKnown() || cm.Private.IsKnown() {
		t.Errorf("magnet: class=%s wanted=%v private=%v", cm.StateClass, cm.WantedBytes, cm.Private)
	}
	if m["d"].Availability.IsKnown() {
		t.Error("distributed_copies 0 must be unknown, not zero availability")
	}
	if m["1"].SwarmSeeds.Status != domain.Unknown {
		t.Error("-1 swarm counts must be unknown")
	}
	for k, want := range map[string]domain.StateClass{"d": domain.StatePaused, "e": domain.StateComplete, "f": domain.StateError,
		"1": domain.StateQueued, "2": domain.StateComplete, "3": domain.StateChecking} {
		if m[k].StateClass != want {
			t.Errorf("%s: %s want %s", k, m[k].StateClass, want)
		}
	}
	if m["3"].VerifiedBytes.IsKnown() {
		t.Error("progress while checking must not be used as verified data")
	}
	if msg, _ := m["f"].ClientMessage.Get(); !strings.Contains(msg, "No such file") {
		t.Errorf("error message %q", msg)
	}
	out, _ := json.Marshal(snap)
	if strings.Contains(string(out), "0123456789abcdef0123456789abcdef") || strings.Contains(string(out), "tracker.example") {
		t.Error("tracker data leaked")
	}
}

func TestDisconnectedWebIsUnsupported(t *testing.T) {
	f := &fakeWeb{password: "pw", connected: false, status: fixture(t)}
	_, err := newClient(t, f, "pw").ListTransfers(context.Background())
	if adapter.ClassOf(err) != adapter.ErrUnsupported || !strings.Contains(err.Error(), "Connection Manager") {
		t.Fatalf("got %v", err)
	}
}

func TestLoginAndSessionExpiry(t *testing.T) {
	f := &fakeWeb{password: "pw", connected: true, status: fixture(t)}
	if _, err := newClient(t, f, "bad").Probe(context.Background()); adapter.ClassOf(err) != adapter.ErrUnauthorized {
		t.Fatalf("bad password: %v", err)
	}
	c := newClient(t, f, "pw")
	if _, err := c.ListTransfers(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.expire.Store(true)
	before := f.logins.Load()
	if _, err := c.ListTransfers(context.Background()); err != nil {
		t.Fatalf("expired session should re-login once: %v", err)
	}
	if f.logins.Load() != before+1 {
		t.Fatalf("logins %d → %d", before, f.logins.Load())
	}
	if _, err := c.rawCall(context.Background(), "core.remove_torrent", nil, nil); adapter.ClassOf(err) != adapter.ErrUnsupported {
		t.Fatalf("write method must be refused locally: %v", err)
	}
}

func TestNullListingIsMalformed(t *testing.T) {
	for _, body := range []string{`{"result":null,"error":null,"id":3}`, `{"error":null,"id":3}`} {
		f := &fakeWeb{password: "pw", connected: true, status: []byte(body)}
		if snap, err := newClient(t, f, "pw").ListTransfers(context.Background()); err == nil {
			t.Fatalf("%s accepted: complete=%v", body, snap.Complete)
		}
	}
	f := &fakeWeb{password: "pw", connected: true, status: []byte(`{"result":{},"error":null,"id":3}`)}
	snap, err := newClient(t, f, "pw").ListTransfers(context.Background())
	if err != nil || !snap.Complete || len(snap.Transfers) != 0 {
		t.Fatalf("explicit empty result: %v %+v", err, snap)
	}
}
