//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// TestAutomaticPreservedDataFailure drives preserved-data failure handling
// through the production binary (`run --once`, repeated): the real engine
// observes a fresh Sonarr grab whose tracker rejects it as not registered,
// diagnoses source_unregistered from its own observations (a transfer
// override shortens grace to 0s and source_confirm_window to 1m, and
// enables the workflow for this transfer only), and automatic dispatch
// fails and blocklists the grab and stops the torrent. Nothing is supplied.
func TestAutomaticPreservedDataFailure(t *testing.T) {
	ctx := context.Background()
	mm := managedManifest(t)
	e := mm["tv-e12-unregistered-auto"]
	a := arrAPI{t: t, base: env(t, "HK_IT_SONARR_URL"), key: env(t, "HK_IT_SONARR_KEY")}
	prepareManager(t, a, env(t, "HK_IT_WORK")+"/library/tv", []int{5000, 5040})
	ep := episodeID(t, a, ensureSeries(t, a), 1, 12)
	waitGrab(t, a, e, map[string]any{"name": "EpisodeSearch", "episodeIds": []int{ep}}, url.Values{"episodeId": {fmt.Sprint(ep)}})
	settleIncomplete(t, e)
	if !settleRejected(t, e) {
		return
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "housekeeparr")
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "../../cmd/housekeeparr")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	clientID := map[string]string{"qbittorrent": "qbit", "transmission": "tr", "deluge": "deluge", "rtorrent": "rt"}[managerClientKind()]
	dcMap, clientBlock := managerTOMLConfig(t)
	stateDir, cfgPath := filepath.Join(dir, "state"), filepath.Join(dir, "housekeeparr.toml")
	var b strings.Builder
	fmt.Fprintf(&b, "version = 1\nmode = \"automatic\"\n[state]\ndir = %q\n[web]\nenabled = false\n[polling]\ninterval = \"1m\"\njitter = \"0s\"\n", stateDir)
	fmt.Fprintf(&b, "[[sonarr]]\nid = \"sonarr\"\nurl = %q\napi_key = \"env:HK_IT_SONARR_KEY\"\n%s\n", env(t, "HK_IT_SONARR_URL"), dcMap)
	b.WriteString(clientBlock)
	fmt.Fprintf(&b, "\n[[override]]\ntransfer = %q\nreason = \"integration test: short confirmation window for this synthetic rejection\"\n"+
		"[override.policy]\nnew_download_grace = \"0s\"\nsource_confirm_window = \"1m\"\nmin_observations = 3\n"+
		"[override.automation]\npreserved_data_failure = true\n", clientID+"|"+e.Hash)
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := payloadDigest(t, e.asEntry())
	hist0 := a.history(e.Hash)

	var intent *store.Intent
	deadline := time.Now().Add(6 * time.Minute)
	for i := 1; intent == nil; i++ {
		out, err := exec.Command(bin, "run", "--once", "--headless", "--config", cfgPath).CombinedOutput()
		if err != nil {
			t.Fatalf("cycle %d: run --once failed: %v\n%s", i, err, lastLines(string(out), 20))
		}
		st, err := store.Open(ctx, stateDir, store.Options{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		tr, _ := st.Transfer(ctx, clientID, e.Hash)
		ins, _ := st.Intents(ctx, store.IntentFilter{})
		st.Close()
		head := "no assessment"
		if tr != nil && tr.Assessment != nil {
			head = string(tr.Assessment.Status) + "/" + string(tr.Assessment.Diagnosis) + ": " + tr.Assessment.Headline
		}
		t.Logf("cycle %d: %s; %d action(s)", i, head, len(ins))
		for k := range ins {
			if ins[k].Workflow == string(policy.WorkflowPreservedDataFailure) {
				intent = &ins[k]
			}
		}
		if intent == nil && time.Now().After(deadline) {
			t.Fatalf("no automatic preserved-data action in time; last: %s\n%s", head, lastLines(string(out), 20))
		}
		if intent == nil {
			time.Sleep(20 * time.Second)
		}
	}
	if intent.State != store.IntentSucceeded || intent.Origin != "automatic" {
		t.Fatalf("action %+v", intent)
	}
	// Independent checks: failed once in the manager, blocklisted, the
	// torrent stopped and registered with its payload intact.
	h := a.history(e.Hash)
	if count(h, "downloadFailed") != count(hist0, "downloadFailed")+1 {
		t.Fatalf("history %v → %v", hist0, h)
	}
	if a.row(nil, e.Hash) != nil {
		t.Fatal("queue row still present")
	}
	tr, ok := clientState(t, e.Hash)
	if !ok || tr.StateClass != domain.StatePaused {
		t.Fatalf("torrent not stopped and registered: %v %s", ok, tr.StateClass)
	}
	if d := payloadDigest(t, e.asEntry()); d != digest {
		t.Fatal("payload changed")
	}
	t.Logf("automatic preserved-data action #%d succeeded: %s", intent.ID, intent.Explanation)
}
