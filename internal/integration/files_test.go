//go:build integration

package integration

import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/adapter/deluge"
	"github.com/voidxela/housekeeparr/internal/adapter/rtorrent"
	"github.com/voidxela/housekeeparr/internal/adapter/transmission"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// TestClientFileLists reads file lists from every released client in the
// run: a single-file torrent, and a multi-file torrent whose sample the
// harness deselected (a selected-file subset), which must classify as a
// video payload, not as sample-only.
func TestClientFileLists(t *testing.T) {
	m := manifest(t)
	listers := map[string]adapter.FileLister{}
	if os.Getenv("HK_IT_QBT_URL") != "" {
		listers["qbittorrent"] = qbit(t)
	}
	if base := os.Getenv("HK_IT_TR_URL"); base != "" {
		c, err := transmission.New(transmission.Options{ID: "tr", BaseURL: base, Username: env(t, "HK_IT_TR_USER"), Password: env(t, "HK_IT_TR_PASS"), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		listers["transmission"] = c
	}
	if base := os.Getenv("HK_IT_DELUGE_URL"); base != "" {
		c, err := deluge.New(deluge.Options{ID: "deluge", BaseURL: base, Password: env(t, "HK_IT_DELUGE_PASS"), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		listers["deluge"] = c
	}
	if base := os.Getenv("HK_IT_RT_URL"); base != "" {
		c, err := rtorrent.New(rtorrent.Options{ID: "rt", BaseURL: base, Username: env(t, "HK_IT_RT_USER"), Password: env(t, "HK_IT_RT_PASS"), Timeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		listers["rtorrent"] = c
	}
	if len(listers) == 0 {
		t.Fatal("no client in this run")
	}
	ctx := context.Background()
	for name, c := range listers {
		t.Run(name, func(t *testing.T) {
			fs, err := c.Files(ctx, m["complete"].Hash)
			if err != nil || len(fs) != 1 || path.Base(fs[0].Path) != m["complete"].Name || fs[0].Size != 4<<20 {
				t.Fatalf("single-file torrent: %+v %v", fs, err)
			}
			if w, ok := fs[0].Wanted.Get(); !ok || !w {
				t.Errorf("single file not reported selected: %+v", fs[0])
			}
			d := m["deselected"]
			fs, err = c.Files(ctx, d.Hash)
			if err != nil || len(fs) != 3 {
				t.Fatalf("multi-file torrent: %+v %v", fs, err)
			}
			for _, f := range fs {
				w, ok := f.Wanted.Get()
				if !ok || w == (path.Base(f.Path) == d.Deselect) {
					t.Errorf("%s: selection %+v (deselected by the harness: %s)", f.Path, f.Wanted, d.Deselect)
				}
			}
			if pc := domain.ClassifyPayload(fs); pc.Kind != domain.PayloadVideo || pc.Deselected != 1 {
				t.Errorf("deselected sample classified %+v", pc)
			}
			t.Logf("%s files: %+v", name, fs)
		})
	}
}
