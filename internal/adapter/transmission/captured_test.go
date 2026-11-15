package transmission

import (
	"context"
	"testing"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Captured, sanitized torrent-get responses from disposable released
// instances (scripts/it/run.sh --clients transmission --capture).
func TestCapturedReleasedResponses(t *testing.T) {
	for _, tc := range []struct{ version, session, file string }{
		{"4.0.6", "v4.0.6-session-get.json", "captured-v4.0.6-torrent-get.json"},
		{"4.1.4", "v4.1.4-session-get.json", "captured-v4.1.4-torrent-get.json"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			f := &fakeTR{session: load(t, tc.session), torrents: load(t, tc.file), sid: "s"}
			c := client(t, f, "", "")
			p, err := c.Probe(context.Background())
			if err != nil || p.Compatibility != adapter.CompatTested {
				t.Fatalf("%s must be recorded as tested: %v %v", tc.version, p.Compatibility, err)
			}
			snap, err := c.ListTransfers(context.Background())
			if err != nil || !snap.Complete || len(snap.Transfers) != 8 {
				t.Fatalf("%d %v", len(snap.Transfers), err)
			}
			states := map[domain.StateClass]int{}
			for _, tr := range snap.Transfers {
				states[tr.StateClass]++
				if tr.StateClass != domain.StateMetadata && !tr.Private.IsKnown() {
					t.Errorf("%s: privacy must be known with metadata: %v", tr.Name, tr.Private)
				}
			}
			want := map[domain.StateClass]int{domain.StateDownloading: 5, domain.StatePaused: 1, domain.StateMetadata: 1, domain.StateComplete: 1}
			for k, n := range want {
				if states[k] != n {
					t.Errorf("%s: %d, want %d (%v)", k, states[k], n, states)
				}
			}
		})
	}
}
