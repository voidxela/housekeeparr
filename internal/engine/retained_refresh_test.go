package engine

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/store"
)

// An unchanged poll writes nothing for retained transfers, however many
// transfers the client lists: the client's small retained set is read once
// and only members whose listed size changed are updated. A changed size
// is still recorded.
func TestUnchangedPollsWriteNoRetainedUpdates(t *testing.T) {
	statements := func(retained int) (unchanged, changed int64) {
		t.Helper()
		cfg, _, err := config.Parse([]byte("version = 1\n[polling]\njitter = \"0s\"\n[[qbittorrent]]\nid = \"q\"\nurl = \"http://127.0.0.1:8080\"\n"), "/x/h.toml")
		if err != nil {
			t.Fatal(err)
		}
		clock := fake.NewClock(t0)
		st, err := store.Open(ctx, t.TempDir(), store.Options{Now: clock.Now})
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		c := fake.NewClient("q")
		var ts []domain.Transfer
		for i := 0; i < 500; i++ {
			ts = append(ts, fake.Transfer(fmt.Sprintf("%040x", i), "T", domain.StateComplete, 2*gib, 2*gib, t0.Add(-time.Hour)))
		}
		c.SetTransfers(ts...)
		if err := st.Write(ctx, func(tx *store.Tx) error {
			for i := 0; i < retained; i++ {
				w := 2 * gib
				if err := tx.RecordRetained(store.RetainedTransfer{ClientID: "q", NativeID: fmt.Sprintf("%040x", i), IntentID: int64(i + 1),
					RetainedAt: t0, Disposition: store.RetainedStopped, WantedBytes: &w, VerifiedBytes: &w}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		e, err := New(ctx, Options{Config: cfg, Store: st, Conns: &Connections{Clients: []adapter.DownloadClient{c},
			Meta: map[string]ConnMeta{"q": {ID: "q", Kind: adapter.KindQBittorrent, Enabled: true}}}, Clock: clock,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), RunKind: "test"})
		if err != nil {
			t.Fatal(err)
		}
		cycle := func() int64 {
			clock.Advance(10 * time.Minute)
			before := st.Statements()
			if rep := e.Cycle(ctx, nil); rep.StoreError != "" {
				t.Fatal(rep.StoreError)
			}
			return st.Statements() - before
		}
		cycle()
		cycle()
		unchanged = cycle()
		if retained > 0 {
			c.Update(fmt.Sprintf("%040x", 0), func(tr *domain.Transfer) { tr.WantedBytes = domain.KnownValue(3 * gib) })
		}
		changed = cycle()
		if retained > 0 {
			recs, _ := st.RetainedTransfers(ctx, false, 0)
			for _, r := range recs {
				if r.NativeID == fmt.Sprintf("%040x", 0) && (r.WantedBytes == nil || *r.WantedBytes != 3*gib) {
					t.Fatalf("changed size not recorded: %+v", r)
				}
			}
		}
		return unchanged, changed
	}
	base, _ := statements(0)
	few, changed := statements(5)
	if few != base {
		t.Fatalf("an unchanged poll with 5 retained transfers ran %d statements, %d with none", few, base)
	}
	if changed <= few {
		t.Fatalf("a changed retained size ran %d statements, unchanged %d", changed, few)
	}
	t.Logf("unchanged poll of 500 transfers: %d write statements (with 0 or 5 retained)", base)
}
