//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/transmission"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// settledListing polls a client until every manifest torrent is listed and
// none is checking, then returns the transfers by hash. A real listing must
// be complete and must not contain tracker data.
func settledListing(t *testing.T, c adapter.DownloadClient, m map[string]entry) map[string]domain.Transfer {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(90 * time.Second)
	for {
		snap, err := c.ListTransfers(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		byHash := map[string]domain.Transfer{}
		settled := true
		for _, tr := range snap.Transfers {
			byHash[tr.NativeID] = tr
			if tr.StateClass == domain.StateChecking || tr.StateClass == domain.StateTransient {
				settled = false
			}
		}
		if len(byHash) >= len(m) && settled || time.Now().After(deadline) {
			if !snap.Complete {
				t.Fatalf("real torrent listing reported incomplete: %v", snap.Limitations)
			}
			b, _ := json.Marshal(snap)
			if strings.Contains(string(b), fakePasskey) || strings.Contains(string(b), "127.0.0.1:9") {
				t.Fatal("normalized snapshot contains tracker data")
			}
			for key, e := range m {
				tr, ok := byHash[e.Hash]
				if !ok {
					t.Errorf("%s: torrent %s not listed", key, e.Hash)
					continue
				}
				t.Logf("%-10s native=%-14s class=%-11s verified=%v wanted=%v avail=%v(%s) private=%v allWanted=%v peers=%v swarm=%v msg=%q",
					key, tr.NativeState, tr.StateClass, tr.VerifiedBytes, tr.WantedBytes, tr.Availability, tr.Availability.Status,
					tr.Private, tr.AllFilesWanted, tr.ConnectedPeers, tr.SwarmSeeds, tr.ClientMessage.Value)
			}
			return byHash
		}
		time.Sleep(2 * time.Second)
	}
}

func TestTransmissionReleasedBehaviour(t *testing.T) {
	ctx := context.Background()
	m := manifest(t)
	base, version := env(t, "HK_IT_TR_URL"), env(t, "HK_IT_TR_VERSION")
	c, err := transmission.New(transmission.Options{ID: "tr", BaseURL: base, Username: env(t, "HK_IT_TR_USER"), Password: env(t, "HK_IT_TR_PASS"), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("probe: %s %s %s, compatibility %s, limitations %v", p.App, p.Version, p.APIVersion, p.Compatibility, p.Limitations)
	if !strings.HasPrefix(p.Version, version+" ") && p.Version != version {
		t.Fatalf("version %q, want %s", p.Version, version)
	}
	bad, err := transmission.New(transmission.Options{ID: "tr", BaseURL: base, Username: "admin", Password: "wrong", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Probe(ctx); adapter.ClassOf(err) != adapter.ErrUnauthorized {
		t.Errorf("wrong password: %v, want unauthorized", err)
	}

	byHash := settledListing(t, c, m)
	for key, e := range m {
		tr, ok := byHash[e.Hash]
		if !ok {
			continue
		}
		switch key {
		case "complete":
			if tr.StateClass != domain.StateComplete || tr.VerifiedBytes != tr.WantedBytes {
				t.Errorf("complete: %s verified %v wanted %v", tr.StateClass, tr.VerifiedBytes, tr.WantedBytes)
			}
		case "half":
			if tr.StateClass != domain.StateDownloading {
				t.Errorf("half: class %s", tr.StateClass)
			}
			v, _ := tr.VerifiedBytes.Get()
			w, _ := tr.WantedBytes.Get()
			if v <= 0 || v >= w {
				t.Errorf("half: verified %d of %d after verify", v, w)
			}
			// desiredAvailable counts connected peers only; there are none.
			if a, ok := tr.Availability.Get(); !ok || a != 0 {
				t.Errorf("half: availability %v (%s), want known 0 with no connected peers", tr.Availability.Value, tr.Availability.Status)
			}
		case "empty":
			if tr.StateClass != domain.StateDownloading || tr.VerifiedBytes.Value != 0 || !tr.VerifiedBytes.IsKnown() {
				t.Errorf("empty: %s %v", tr.StateClass, tr.VerifiedBytes)
			}
		case "private":
			if !tr.Private.IsKnown() || !tr.Private.Value {
				t.Errorf("private: %v", tr.Private)
			}
		case "deselected":
			if all, ok := tr.AllFilesWanted.Get(); !ok || all {
				t.Errorf("deselected: AllFilesWanted %v", tr.AllFilesWanted)
			}
		case "stopped":
			if tr.StateClass != domain.StatePaused || tr.Availability.IsKnown() {
				t.Errorf("stopped: %s (%s) availability %v", tr.StateClass, tr.NativeState, tr.Availability)
			}
		case "errored", "errored-restart", "errored-recheck":
			// Payload deleted after verification (then started, client
			// restarted, or force-rechecked): Transmission reports a local
			// error (error = 3) with an explanation.
			if tr.StateClass != domain.StateError || tr.ClientMessage.Value == "" {
				t.Errorf("%s: %s (%s) message %q, want a local error", key, tr.StateClass, tr.NativeState, tr.ClientMessage.Value)
			}
		case "magnet":
			if tr.StateClass != domain.StateMetadata || tr.WantedBytes.IsKnown() || tr.Private.IsKnown() || tr.Availability.IsKnown() {
				t.Errorf("magnet: %s wanted %v private %v availability %v", tr.StateClass, tr.WantedBytes, tr.Private, tr.Availability)
			}
		default:
			if key != "movie" && tr.StateClass == domain.StateUnknown {
				t.Errorf("%s: unknown state %s", key, tr.NativeState)
			}
		}
		if key != "magnet" {
			if pub, ok := tr.Private.Get(); !ok || pub != e.Private {
				t.Errorf("%s: privacy %v, want known %v", key, tr.Private, e.Private)
			}
		}
	}
	raw := transmissionRaw(t, base)
	for _, tor := range raw {
		for _, f := range transmission.RequestedFields() {
			if _, ok := tor[f]; !ok {
				t.Errorf("released %s omits requested field %q (%v)", version, f, tor["hashString"])
			}
		}
	}
	if dir := os.Getenv("HK_IT_CAPTURE_DIR"); dir != "" {
		work := env(t, "HK_IT_WORK")
		out, _ := json.MarshalIndent(map[string]any{"result": "success", "arguments": map[string]any{"torrents": raw}}, "", " ")
		s := strings.ReplaceAll(strings.ReplaceAll(string(out), fakePasskey, "[passkey]"), work, "[work]")
		name := filepath.Join(dir, "transmission-v"+version+"-torrent-get.json")
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(name, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("captured %s (review before committing)", name)
	}
}

// transmissionRaw sends the adapter's exact torrent-get request directly and
// returns the raw torrent objects.
func transmissionRaw(t *testing.T, base string) []map[string]any {
	t.Helper()
	var out struct {
		Torrents []map[string]any `json:"torrents"`
	}
	if err := json.Unmarshal(transmissionCall(t, base, "torrent-get", map[string]any{"fields": transmission.RequestedFields()}), &out); err != nil {
		t.Fatalf("raw torrent-get: %v", err)
	}
	return out.Torrents
}

// transmissionCall sends one legacy RPC request directly to the disposable
// daemon (harness side, never through Housekeeparr) and returns its
// arguments.
func transmissionCall(t *testing.T, base, method string, args map[string]any) json.RawMessage {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"method": method, "arguments": args})
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(env(t, "HK_IT_TR_USER")+":"+env(t, "HK_IT_TR_PASS")))
	sid := ""
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodPost, base+"/transmission/rpc", bytes.NewReader(body))
		req.Header.Set("Authorization", auth)
		req.Header.Set("X-Transmission-Session-Id", sid)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusConflict {
			sid = resp.Header.Get("X-Transmission-Session-Id")
			continue
		}
		var out struct {
			Result    string          `json:"result"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(b, &out); err != nil || out.Result != "success" {
			t.Fatalf("raw %s: %v %s", method, err, b)
		}
		return out.Arguments
	}
	t.Fatal("session handshake failed")
	return nil
}
