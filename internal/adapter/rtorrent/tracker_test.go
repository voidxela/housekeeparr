package rtorrent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// trackerRowsXML is what the fake server answers to t.multicall.
var trackerRowsXML = rows()

func trackerRows() string { return trackerRowsXML }

// rows encodes [is_enabled, failed_time_last, success_time_last] rows.
func rows(rs ...[3]int64) string {
	var b strings.Builder
	b.WriteString("<methodResponse><params><param><value><array><data>")
	for _, r := range rs {
		b.WriteString("<value><array><data>")
		for _, x := range r {
			fmt.Fprintf(&b, "<value><i8>%d</i8></value>", x)
		}
		b.WriteString("</data></array></value>")
	}
	b.WriteString("</data></array></value></param></params></methodResponse>")
	return b.String()
}

// A "Tracker:" d.message counts only while an enabled tracker's latest
// failure is newer than its latest success and recent (SPEC §7.2): the
// message alone can survive a user stop/start.
func TestTrackerRejectionFreshness(t *testing.T) {
	now := time.Now().Unix()
	const tracked = "2222222222222222222222222222222222222222"
	for _, tc := range []struct {
		name  string
		rows  []([3]int64)
		want  domain.SourceFailure
		known bool
	}{
		{"recent failure, never succeeded", [][3]int64{{1, now - 60, 0}}, domain.SourceUnregistered, true},
		{"failure newer than success", [][3]int64{{1, now - 60, now - 3600}}, domain.SourceUnregistered, true},
		{"success newer than failure", [][3]int64{{1, now - 600, now - 60}}, domain.SourceNoFailure, true},
		{"another enabled tracker works", [][3]int64{{1, now - 60, 0}, {1, 0, now - 30}}, domain.SourceNoFailure, true},
		{"failure too old (stale message)", [][3]int64{{1, now - 7200, 0}}, "", false},
		{"only disabled trackers", [][3]int64{{0, now - 60, 0}}, "", false},
		{"no trackers", nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trackerRowsXML = rows(tc.rows...)
			t.Cleanup(func() { trackerRowsXML = rows() })
			snap, err := newClient(t, server(t, "", "", nil), "", "").ListTransfers(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, tr := range snap.Transfers {
				v, ok := tr.SourceFailure.Get()
				if tr.NativeID == tracked {
					if ok != tc.known || v != tc.want {
						t.Fatalf("source %+v", tr.SourceFailure)
					}
					continue
				}
				// Active downloads without a tracker message: none; others unknown.
				if tr.StateClass == domain.StateDownloading && (!ok || v != domain.SourceNoFailure) {
					t.Errorf("%s: %+v", tr.Name, tr.SourceFailure)
				}
				if tr.StateClass != domain.StateDownloading && ok {
					t.Errorf("%s (%s) classified: %+v", tr.Name, tr.StateClass, tr.SourceFailure)
				}
			}
		})
	}
	t.Run("unreadable tracker list keeps the listing", func(t *testing.T) {
		snap, err := newClient(t, server(t, "", "", map[string]string{"t.multicall": "<methodResponse><params"}), "", "").ListTransfers(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, tr := range snap.Transfers {
			if tr.NativeID == tracked && tr.SourceFailure.IsKnown() {
				t.Fatalf("%+v", tr.SourceFailure)
			}
		}
	})
}
