package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/demo"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

const qbitFixture = "../../internal/adapter/qbittorrent/testdata/v5.3.0-torrents_info.json"

// fixtureServers serves the synthetic adapter fixtures over HTTP so the real
// adapters, engine, store, and CLI run end to end. Any non-GET request other
// than the qBittorrent login is counted as a forbidden write.
func fixtureServers(t *testing.T) (qbit, sonarr string, writes *atomic.Int32) {
	t.Helper()
	writes = &atomic.Int32{}
	read := func(p string) []byte {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	torrents := read(qbitFixture)
	q := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_ = r.ParseForm()
			if r.Form.Get("password") != "qbit-secret" {
				_, _ = w.Write([]byte("Fails."))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x", Path: "/"})
			_, _ = w.Write([]byte("Ok."))
			return
		}
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		if _, err := r.Cookie("SID"); err != nil {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/api/v2/app/version":
			_, _ = w.Write([]byte("v5.3.0"))
		case "/api/v2/app/webapiVersion":
			_, _ = w.Write([]byte("2.11.4"))
		case "/api/v2/torrents/info":
			_, _ = w.Write(torrents)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(q.Close)
	sdir := "../../internal/adapter/sonarr/testdata/"
	status, page1, page2, health := read(sdir+"v4.0.20-system_status.json"), read(sdir+"v4.0.20-queue_page1.json"), read(sdir+"v4.0.20-queue_page2.json"), read(sdir+"v4.0.20-health.json")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes.Add(1)
		}
		if r.Header.Get("X-Api-Key") != "sonarr-secret" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/system/status":
			_, _ = w.Write(status)
		case "/api/v3/health":
			_, _ = w.Write(health)
		case "/api/v3/queue":
			if r.URL.Query().Get("page") == "2" {
				_, _ = w.Write(page2)
			} else {
				_, _ = w.Write(page1)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	return q.URL, s.URL, writes
}

func writeConfig(t *testing.T, qbit, sonarr string) string {
	t.Helper()
	dir := t.TempDir()
	pw := filepath.Join(dir, "qbit-password")
	if err := os.WriteFile(pw, []byte("qbit-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_SONARR_KEY", "sonarr-secret")
	cfg := `version = 1
[state]
dir = "state"
[[sonarr]]
id = "sonarr"
url = "` + sonarr + `"
api_key = "env:TEST_SONARR_KEY"
queue_page_size = 10
download_clients = { "qBittorrent" = "qbit" }
[[qbittorrent]]
id = "qbit"
url = "` + qbit + `"
username = "admin"
password = "file:` + pw + `"
`
	p := filepath.Join(dir, "housekeeparr.toml")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := dispatch(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestEndToEndOneShotReadOnlySlice(t *testing.T) {
	qbit, sonarr, writes := fixtureServers(t)
	cfg := writeConfig(t, qbit, sonarr)

	if code, out, errOut := runCLI(t, "config", "validate", "--config", cfg); code != exitOK {
		t.Fatalf("config validate: %d\n%s%s", code, out, errOut)
	}
	code, out, errOut := runCLI(t, "check", "--config", cfg)
	if code != exitOK || !strings.Contains(out, "compatibility: tested") || !strings.Contains(out, "7 transfers listed") {
		t.Fatalf("check: %d\n%s%s", code, out, errOut)
	}
	code, out, errOut = runCLI(t, "run", "--once", "--config", cfg)
	if code != exitOK {
		t.Fatalf("run --once: %d\n%s%s", code, out, errOut)
	}
	if strings.Contains(errOut+out, "qbit-secret") || strings.Contains(errOut+out, "sonarr-secret") {
		t.Fatal("secret value leaked into output")
	}
	code, out, _ = runCLI(t, "findings", "--json", "--config", cfg)
	if code != exitOK {
		t.Fatalf("findings: %d", code)
	}
	var fs []struct {
		Type    string `json:"type"`
		Subject struct {
			Source string `json:"source"`
		} `json:"subject"`
	}
	if err := json.Unmarshal([]byte(out), &fs); err != nil {
		t.Fatal(err)
	}
	health := 0
	for _, f := range fs {
		if f.Type == "manager.health" {
			health++
		}
		if f.Type == "transfer.stalled" {
			t.Fatal("a single observation must never establish a stall")
		}
	}
	if health != 2 {
		t.Fatalf("expected 2 health findings (warning + error; notices excluded), got %d: %s", health, out)
	}
	code, out, _ = runCLI(t, "downloads", "--json", "--config", cfg)
	if code != exitOK || !strings.Contains(out, `"match_status": "matched"`) {
		t.Fatalf("downloads: %d\n%s", code, out)
	}
	if strings.Contains(out, "0123456789abcdef0123456789abcdef") || strings.Contains(out, "/downloads/") {
		t.Fatal("tracker passkey or path leaked into state")
	}
	if code, out, _ := runCLI(t, "status", "--config", cfg); code != exitOK || !strings.Contains(out, "scheduled") {
		t.Fatalf("status after one-shot: %d\n%s", code, out)
	}
	if writes.Load() != 0 {
		t.Fatalf("%d operational writes were sent", writes.Load())
	}
}

func TestCLIErrorsAndExitCodes(t *testing.T) {
	if code, _, _ := runCLI(t); code != exitUsage {
		t.Fatalf("no args: %d", code)
	}
	if code, _, _ := runCLI(t, "bogus"); code != exitUsage {
		t.Fatalf("unknown command: %d", code)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.toml")
	_ = os.WriteFile(bad, []byte("version = 1\nmode = \"autopilot\"\n"), 0o600)
	if code, out, _ := runCLI(t, "config", "validate", "--config", bad); code != exitUsage || !strings.Contains(out, "must be \"observe\", \"paused\", or \"automatic\"") {
		t.Fatalf("invalid config: %d %s", code, out)
	}
	ok := filepath.Join(dir, "ok.toml")
	_ = os.WriteFile(ok, []byte("version = 1\n[state]\ndir = \"st\"\n"), 0o600)
	if code, _, errOut := runCLI(t, "status", "--config", ok); code != exitProblem || !strings.Contains(errOut, "no state database") {
		t.Fatalf("status without state: %d %s", code, errOut)
	}
	if code, _, _ := runCLI(t, "finding", "abc", "--config", ok); code != exitUsage {
		t.Fatalf("bad id: %d", code)
	}
	if code, out, _ := runCLI(t, "policy", "--config", ok); code != exitOK || !strings.Contains(out, "no_progress_window") {
		t.Fatalf("policy: %d %s", code, out)
	}
}

func TestPreviewActionsAndActConfirmation(t *testing.T) {
	dir := t.TempDir()
	d, err := demo.Generate(context.Background(), dir, time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	fs, _ := d.Store.Findings(context.Background(), store.FindingFilter{})
	d.Store.Close()
	var id int64
	for _, f := range fs {
		if f.Type == domain.FindingTransferStalled {
			id = f.ID
		}
	}
	cfg := filepath.Join(dir, "housekeeparr.toml")
	if err := os.WriteFile(cfg, []byte(demo.Config), 0o600); err != nil {
		t.Fatal(err)
	}
	sid := strconv.FormatInt(id, 10)
	code, out, errOut := runCLI(t, "preview", sid, "--config", cfg)
	if code != exitOK || !strings.Contains(out, "NOT ELIGIBLE") || !strings.Contains(out, "[FAIL] tested write workflow") ||
		!strings.Contains(out, `"removeFromClient":false`) || !strings.Contains(out, "Nothing will be sent") {
		t.Fatalf("preview: %d\n%s%s", code, out, errOut)
	}
	if code, out, _ := runCLI(t, "preview", sid, "--json", "--automatic", "--config", cfg); code != exitOK || !strings.Contains(out, `"origin": "automatic"`) {
		t.Fatalf("preview --json: %d %s", code, out)
	}
	if code, _, errOut := runCLI(t, "act", sid, "--config", cfg); code != exitUsage || !strings.Contains(errOut, "--confirm") {
		t.Fatalf("act without --confirm: %d %s", code, errOut)
	}
	if code, out, _ := runCLI(t, "actions", "--config", cfg); code != exitOK || !strings.Contains(out, "No actions") {
		t.Fatalf("actions: %d %s", code, out)
	}
	if code, _, _ := runCLI(t, "restore-ack", "--config", cfg); code != exitUsage {
		t.Fatalf("restore-ack without --confirm: %d", code)
	}
}
