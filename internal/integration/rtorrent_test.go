//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/rtorrent"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// TestRTorrentReleasedBehaviour exercises rTorrent through a real lighttpd
// SCGI gateway with HTTP basic authentication.
func TestRTorrentReleasedBehaviour(t *testing.T) {
	ctx := context.Background()
	m := manifest(t)
	base, version := env(t, "HK_IT_RT_URL"), env(t, "HK_IT_RT_VERSION")
	c, err := rtorrent.New(rtorrent.Options{ID: "rt", BaseURL: base, Username: env(t, "HK_IT_RT_USER"), Password: env(t, "HK_IT_RT_PASS"), Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Probe(ctx)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	t.Logf("probe: %s %s %s, compatibility %s, limitations %v", p.App, p.Version, p.APIVersion, p.Compatibility, p.Limitations)
	if p.Version != version {
		t.Fatalf("version %q, want %s", p.Version, version)
	}
	bad, err := rtorrent.New(rtorrent.Options{ID: "rt", BaseURL: base, Username: "admin", Password: "wrong", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Probe(ctx); adapter.ClassOf(err) != adapter.ErrUnauthorized {
		t.Errorf("wrong gateway password: %v, want unauthorized", err)
	}

	byHash := settledListing(t, c, m)
	for key, e := range m {
		tr, ok := byHash[e.Hash]
		if !ok {
			continue
		}
		if tr.Availability.Status != domain.Unsupported {
			t.Errorf("%s: availability must be unsupported, got %v", key, tr.Availability)
		}
		switch key {
		case "complete":
			if tr.StateClass != domain.StateComplete || tr.VerifiedBytes != tr.WantedBytes {
				t.Errorf("complete: %s verified %v wanted %v", tr.StateClass, tr.VerifiedBytes, tr.WantedBytes)
			}
		case "half":
			v, _ := tr.VerifiedBytes.Get()
			w, _ := tr.WantedBytes.Get()
			if tr.StateClass != domain.StateDownloading || v <= 0 || v >= w {
				t.Errorf("half: %s verified %d of %d after hash check", tr.StateClass, v, w)
			}
		case "empty":
			if tr.StateClass != domain.StateDownloading || !tr.VerifiedBytes.IsKnown() || tr.VerifiedBytes.Value != 0 {
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
			if tr.StateClass != domain.StatePaused {
				t.Errorf("stopped: %s (%s)", tr.StateClass, tr.NativeState)
			}
		case "errored", "errored-restart", "errored-recheck":
			// Payload deleted after verification (then started, client
			// restarted, or d.check_hash): rTorrent stops the download itself
			// and explains why in d.message.
			if tr.StateClass != domain.StateError || tr.ClientMessage.Value == "" {
				t.Errorf("%s: %s (%s) message %q, want a client error", key, tr.StateClass, tr.NativeState, tr.ClientMessage.Value)
			}
		case "magnet":
			if tr.StateClass != domain.StateMetadata || tr.WantedBytes.IsKnown() || tr.Private.IsKnown() {
				t.Errorf("magnet: %s (%s) wanted %v private %v", tr.StateClass, tr.NativeState, tr.WantedBytes, tr.Private)
			}
		}
		if key != "magnet" {
			if pub, ok := tr.Private.Get(); !ok || pub != e.Private {
				t.Errorf("%s: privacy %v, want known %v", key, tr.Private, e.Private)
			}
		}
	}

	t.Run("d_message_stop_start_probe", func(t *testing.T) {
		e, ok := m["rt-msg-probe"]
		if !ok {
			t.Fatal("rt-msg-probe fixture missing from manifest")
		}
		saveDir := env(t, "HK_IT_RT_SAVE")
		user, pass := env(t, "HK_IT_RT_USER"), env(t, "HK_IT_RT_PASS")
		h := strings.ToUpper(e.Hash)
		payloadPath := filepath.Join(saveDir, e.Name)

		logState := func(label string) domain.Transfer {
			t.Helper()
			snap, err := c.ListTransfers(ctx)
			if err != nil {
				t.Fatalf("%s: list: %v", label, err)
			}
			var found domain.Transfer
			for _, tr := range snap.Transfers {
				if tr.NativeID == e.Hash {
					found = tr
					break
				}
			}
			st := rtXMLRPC(t, base, user, pass, "d.state", h)
			act := rtXMLRPC(t, base, user, pass, "d.is_active", h)
			comp := rtXMLRPC(t, base, user, pass, "d.complete", h)
			msg := rtXMLRPC(t, base, user, pass, "d.message", h)
			t.Logf("[probe] %-30s d.state=%s d.is_active=%s d.complete=%s d.message=%q | class=%s native=%s clientMsg=%q",
				label, st, act, comp, msg, found.StateClass, found.NativeState, found.ClientMessage.Value)
			return found
		}

		tr := logState("1.initial-healthy")
		if tr.StateClass != domain.StateComplete {
			t.Fatalf("rt-msg-probe initial class=%s, want complete", tr.StateClass)
		}

		rtXMLRPC(t, base, user, pass, "d.stop", h)
		time.Sleep(1 * time.Second)
		tr = logState("2.healthy-after-d.stop")
		if tr.ClientMessage.Value != "" {
			t.Logf("[probe] NOTE: healthy torrent after d.stop has non-empty d.message=%q", tr.ClientMessage.Value)
		}

		rtXMLRPC(t, base, user, pass, "d.start", h)
		time.Sleep(1 * time.Second)
		logState("3.healthy-after-d.start")

		savedBytes, err := os.ReadFile(payloadPath)
		if err != nil {
			t.Fatalf("read payload: %v", err)
		}
		rtXMLRPC(t, base, user, pass, "d.stop", h)
		rtXMLRPC(t, base, user, pass, "d.close", h)
		time.Sleep(1 * time.Second)
		if err := os.Remove(payloadPath); err != nil {
			t.Fatalf("remove payload: %v", err)
		}
		rtXMLRPC(t, base, user, pass, "d.start", h)
		time.Sleep(3 * time.Second)
		logState("4.errored-missing-payload")

		rtXMLRPC(t, base, user, pass, "d.stop", h)
		time.Sleep(1 * time.Second)
		logState("5a.errored-after-d.stop")
		rtXMLRPC(t, base, user, pass, "d.start", h)
		logState("5b.errored-after-d.start-immed")
		time.Sleep(2 * time.Second)
		logState("5c.errored-after-d.start-2s")

		rtXMLRPC(t, base, user, pass, "d.close", h)
		time.Sleep(1 * time.Second)
		logState("6a.errored-after-d.close")
		rtXMLRPC(t, base, user, pass, "d.open", h)
		time.Sleep(1 * time.Second)
		logState("6b.errored-after-d.open")
		rtXMLRPC(t, base, user, pass, "d.start", h)
		time.Sleep(2 * time.Second)
		logState("6c.errored-after-d.start")

		// Restore payload, recheck, start, and then user-stop to see if a
		// stale d.message survives and makes a genuine pause look like an error.
		if err := os.WriteFile(payloadPath, savedBytes, 0o644); err != nil {
			t.Fatalf("restore payload: %v", err)
		}
		rtXMLRPC(t, base, user, pass, "d.check_hash", h)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if rtXMLRPC(t, base, user, pass, "d.hashing", h) == "0" {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		logState("7a.restored-after-recheck")
		rtXMLRPC(t, base, user, pass, "d.start", h)
		time.Sleep(1 * time.Second)
		logState("7b.restored-after-d.start")
		rtXMLRPC(t, base, user, pass, "d.stop", h)
		time.Sleep(1 * time.Second)
		logState("7c.restored-after-d.stop")
	})
}

func rtXMLRPC(t *testing.T, base, user, pass, method, hash string) string {
	t.Helper()
	body := fmt.Sprintf(`<?xml version="1.0"?><methodCall><methodName>%s</methodName><params><param><value><string>%s</string></value></param></params></methodCall>`, method, hash)
	req, err := http.NewRequest(http.MethodPost, base+"/RPC2", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("xmlrpc %s: %v", method, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("xmlrpc %s: HTTP %d %s", method, resp.StatusCode, string(b))
	}
	var res struct {
		Value struct {
			Str string `xml:"string"`
			I4  string `xml:"i4"`
			Int string `xml:"int"`
			I8  string `xml:"i8"`
			Raw string `xml:",chardata"`
		} `xml:"params>param>value"`
	}
	if err := xml.Unmarshal(b, &res); err != nil {
		t.Fatalf("xmlrpc %s decode %s: %v", method, string(b), err)
	}
	for _, s := range []string{res.Value.Str, res.Value.I8, res.Value.Int, res.Value.I4, strings.TrimSpace(res.Value.Raw)} {
		if s != "" {
			return s
		}
	}
	return ""
}
