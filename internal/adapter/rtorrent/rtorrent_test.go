package rtorrent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

type methodCall struct {
	Method string   `xml:"methodName"`
	Params []string `xml:"params>param>value>string"`
}

func server(t *testing.T, user, pass string, override map[string]string) *httptest.Server {
	load := func(n string) string {
		b, err := os.ReadFile("testdata/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/RPC2" || r.Method != http.MethodPost {
			w.WriteHeader(404)
			return
		}
		if user != "" && r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)) {
			w.WriteHeader(401)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var mc methodCall
		if err := xml.Unmarshal(body, &mc); err != nil {
			t.Errorf("bad request XML: %v", err)
		}
		if o, ok := override[mc.Method]; ok {
			_, _ = w.Write([]byte(o))
			return
		}
		switch mc.Method {
		case "system.client_version":
			_, _ = w.Write([]byte(load("v0.16.28-client_version.xml")))
		case "system.library_version":
			_, _ = w.Write([]byte(strings.Replace(load("v0.16.28-client_version.xml"), "0.16.28", "0.16.6", 1)))
		case "d.multicall2":
			for _, p := range mc.Params[2:] {
				if !strings.HasPrefix(p, "d.") || strings.Contains(p, ".set") || strings.HasPrefix(p, "d.erase") || strings.HasPrefix(p, "d.stop") {
					t.Errorf("non-read command in multicall: %s", p)
				}
			}
			_, _ = w.Write([]byte(load("v0.16.28-d.multicall2.xml")))
		default:
			t.Errorf("unexpected method %s", mc.Method)
			_, _ = w.Write([]byte(load("fault.xml")))
		}
	}))
}

func newClient(t *testing.T, srv *httptest.Server, user, pass string) *Client {
	t.Cleanup(srv.Close)
	c, err := New(Options{ID: "rt", BaseURL: srv.URL, Username: user, Password: pass, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestContract(t *testing.T) {
	c := newClient(t, server(t, "u", "p", nil), "u", "p")
	ctx := context.Background()
	p, err := c.Probe(ctx)
	if err != nil || p.Version != "0.16.28" || p.APIVersion != "libtorrent 0.16.6" || p.Compatibility != adapter.CompatUntested {
		t.Fatalf("%+v %v", p, err)
	}
	if st := p.Write[adapter.CapStopTransfer]; st.Support != adapter.Supported || !strings.Contains(st.Note, "d.stop") {
		t.Fatalf("stop capability: %+v", st)
	}
	if p.Write[adapter.CapTransferLabel].Support != adapter.NotImplemented || len(p.Write) != 2 {
		t.Fatalf("write capabilities: %+v", p.Write)
	}
	snap, err := c.ListTransfers(ctx)
	if err != nil || len(snap.Transfers) != 8 {
		t.Fatalf("%d %v", len(snap.Transfers), err)
	}
	m := map[string]domain.Transfer{}
	for _, tr := range snap.Transfers {
		m[tr.NativeID[:1]] = tr
	}
	a := m["a"]
	if a.StateClass != domain.StateDownloading || a.VerifiedBytes.Value != 1<<28 || !a.AllFilesWanted.Value || a.Category.Value != "tv-sonarr" || a.ConnectedSeeds.Value != 1 {
		t.Errorf("a: %+v", a)
	}
	if a.Availability.Status != domain.Unsupported {
		t.Error("availability must be unsupported, never zero")
	}
	b := m["b"]
	if b.AllFilesWanted.Value || b.WantedBytes.Value != 2<<30+6<<30 || !b.Private.Value || b.WantedBytes.Note == "" {
		t.Errorf("b (deselected): wanted=%v all=%v private=%v", b.WantedBytes, b.AllFilesWanted, b.Private)
	}
	if cm := m["c"]; cm.StateClass != domain.StateMetadata || cm.WantedBytes.IsKnown() || cm.Private.IsKnown() {
		t.Errorf("meta: %+v", cm)
	}
	for k, want := range map[string]domain.StateClass{"d": domain.StatePaused, "e": domain.StatePaused, "f": domain.StateComplete, "1": domain.StateChecking, "2": domain.StateDownloading} {
		if m[k].StateClass != want {
			t.Errorf("%s: %s want %s", k, m[k].StateClass, want)
		}
	}
	out, _ := json.Marshal(snap)
	if strings.Contains(string(out), "0123456789abcdef0123456789abcdef") {
		t.Error("passkey leaked from d.message")
	}
	if msg, _ := m["2"].ClientMessage.Get(); !strings.Contains(msg, "unregistered torrent") {
		t.Errorf("tracker message %q", msg)
	}
}

func TestFaultsErrorsAndVersions(t *testing.T) {
	load := func(n string) string { b, _ := os.ReadFile("testdata/" + n); return string(b) }
	c := newClient(t, server(t, "", "", map[string]string{"d.multicall2": load("fault.xml")}), "", "")
	if _, err := c.ListTransfers(context.Background()); adapter.ClassOf(err) != adapter.ErrUnsupported {
		t.Fatalf("fault: %v", err)
	}
	c = newClient(t, server(t, "", "", map[string]string{"d.multicall2": "<methodResponse><params><param><value><array><data><value><array><data><value>x</value></data></array></value></data></array></value></param></params></methodResponse>"}), "", "")
	if _, err := c.ListTransfers(context.Background()); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("short row: %v", err)
	}
	c = newClient(t, server(t, "", "", map[string]string{"d.multicall2": "<methodResponse><params"}), "", "")
	if _, err := c.ListTransfers(context.Background()); adapter.ClassOf(err) != adapter.ErrPermanent {
		t.Fatalf("malformed xml: %v", err)
	}
	c = newClient(t, server(t, "", "", map[string]string{"system.client_version": "<methodResponse><params><param><value>0.9.6</value></param></params></methodResponse>"}), "", "")
	if p, err := c.Probe(context.Background()); err != nil || p.Compatibility != adapter.CompatUnsupported {
		t.Fatalf("old version: %v %v", p.Compatibility, err)
	}
	c = newClient(t, server(t, "u", "p", nil), "u", "wrong")
	if _, err := c.Probe(context.Background()); adapter.ClassOf(err) != adapter.ErrUnauthorized {
		t.Fatalf("auth: %v", err)
	}
	if _, err := c.call(context.Background(), "d.erase", "x"); adapter.ClassOf(err) != adapter.ErrUnsupported {
		t.Fatalf("write method must be refused locally: %v", err)
	}
}

func TestVersionCompare(t *testing.T) {
	for v, want := range map[string]bool{"0.9.8": true, "0.9.7": false, "0.10.0": true, "0.16.28": true, "1.0": true, "0.9": false} {
		if got := versionAtLeast(v, MinimumVersion); got != want {
			t.Errorf("%s: %v", v, got)
		}
	}
}

// A numeric column of the wrong type must not silently become 0 (which
// would read as "metadata present" or "public").
func TestNonIntegerColumnIsMalformed(t *testing.T) {
	b, err := os.ReadFile("testdata/v0.16.28-d.multicall2.xml")
	if err != nil {
		t.Fatal(err)
	}
	// Replace the first <i8> value with a string.
	s := string(b)
	i := strings.Index(s, "<i8>")
	j := strings.Index(s[i:], "</i8>") + i + len("</i8>")
	bad := s[:i] + "<string>x</string>" + s[j:]
	c := newClient(t, server(t, "", "", map[string]string{"d.multicall2": bad}), "", "")
	if _, err := c.ListTransfers(context.Background()); err == nil {
		t.Fatal("non-integer column accepted")
	}
	empty := `<?xml version="1.0"?><methodResponse><params><param><value><array><data></data></array></value></param></params></methodResponse>`
	c = newClient(t, server(t, "", "", map[string]string{"d.multicall2": empty}), "", "")
	snap, err := c.ListTransfers(context.Background())
	if err != nil || !snap.Complete || len(snap.Transfers) != 0 {
		t.Fatalf("explicit empty array: %v %+v", err, snap)
	}
}

// A download rTorrent stopped itself, with a local (non-tracker) message, is
// a client error rather than a user pause; a tracker message on a stopped
// download is not.
func TestStoppedWithLocalMessageIsError(t *testing.T) {
	row := func(state int64, msg string) []any {
		c := make([]any, len(fields))
		for _, i := range numericColumns {
			c[i] = int64(0)
		}
		c[0], c[1], c[19], c[20] = strings.Repeat("a", 40), "x", msg, ""
		c[2], c[3], c[7], c[10], c[12], c[13] = state, int64(0), int64(1<<20), int64(4), int64(4), int64(1<<18)
		return c
	}
	for _, tc := range []struct {
		state int64
		msg   string
		want  domain.StateClass
	}{
		{0, "Download registered as completed, but hash check returned unfinished chunks.", domain.StateError},
		{0, `Tracker: [Failure reason "unregistered torrent"]`, domain.StatePaused},
		{0, "", domain.StatePaused},
	} {
		tr, err := normalize("rt", row(tc.state, tc.msg))
		if err != nil || tr.StateClass != tc.want {
			t.Errorf("%q: %s %v, want %s", tc.msg, tr.StateClass, err, tc.want)
		}
	}
}
