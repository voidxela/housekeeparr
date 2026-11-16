//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/deluge"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// TestDelugeReleasedBehaviour exercises deluged through a real deluge-web
// JSON API that the harness (not Housekeeparr) connected to the daemon.
func TestDelugeReleasedBehaviour(t *testing.T) {
	ctx := context.Background()
	m := manifest(t)
	base, version := env(t, "HK_IT_DELUGE_URL"), env(t, "HK_IT_DELUGE_VERSION")
	c, err := deluge.New(deluge.Options{ID: "deluge", BaseURL: base, Password: env(t, "HK_IT_DELUGE_PASS"), Timeout: 10 * time.Second})
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
	bad, err := deluge.New(deluge.Options{ID: "deluge", BaseURL: base, Password: "wrong", Timeout: 10 * time.Second})
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
			v, _ := tr.VerifiedBytes.Get()
			w, _ := tr.WantedBytes.Get()
			if tr.StateClass != domain.StateDownloading || v <= 0 || v >= w {
				t.Errorf("half: %s verified %d of %d after recheck", tr.StateClass, v, w)
			}
		case "empty":
			if tr.StateClass != domain.StateDownloading || !tr.VerifiedBytes.IsKnown() || tr.VerifiedBytes.Value != 0 {
				t.Errorf("empty: %s %v", tr.StateClass, tr.VerifiedBytes)
			}
			if tr.Availability.IsKnown() {
				t.Errorf("empty: distributed_copies 0 must be unknown, got %v", tr.Availability)
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
		case "errored":
			// Payload deleted after verification. libtorrent does not notice
			// until it reads the data, so qBittorrent and Deluge still report
			// the torrent complete: "complete" is not evidence that data exists.
			if tr.StateClass != domain.StateComplete {
				t.Errorf("errored: %s (%s); released behaviour differs from the recorded semantics", tr.StateClass, tr.NativeState)
			}
		case "errored-restart":
			// Verified Seeding torrent whose payload is deleted while deluged is
			// stopped cleanly: on restart fastresume is rejected and Deluge
			// reports Error ("Missing or invalid torrent data!").
			if tr.StateClass != domain.StateError || tr.ClientMessage.Value == "" {
				t.Errorf("errored-restart: %s (%s) message %q, want StateError", tr.StateClass, tr.NativeState, tr.ClientMessage.Value)
			}
		case "errored-recheck":
			// Forced recheck while running after payload deletion resets progress
			// to 0 and leaves Deluge in Downloading with no error; do not assert error.
			if tr.StateClass != domain.StateDownloading || tr.VerifiedBytes.Value != 0 {
				t.Errorf("errored-recheck: %s (%s) verified=%v, expected Downloading/0", tr.StateClass, tr.NativeState, tr.VerifiedBytes)
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
}
