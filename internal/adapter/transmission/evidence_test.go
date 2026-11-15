package transmission

import (
	"context"
	"testing"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/domain"
)

// Synthetic responses: a success envelope must carry an explicit torrents
// list before absence of a transfer can mean anything.
func TestTorrentListMustBePresent(t *testing.T) {
	for _, body := range []string{
		`{"result":"success","arguments":{}}`,
		`{"result":"success","arguments":{"torrents":null}}`,
		`{"result":"success","arguments":null}`,
		`{"result":"success"}`,
	} {
		t.Run(body, func(t *testing.T) {
			f := &fakeTR{torrents: []byte(body), sid: "s"}
			snap, err := client(t, f, "", "").ListTransfers(context.Background())
			if err == nil {
				t.Fatalf("accepted as listing: complete=%v transfers=%d", snap.Complete, len(snap.Transfers))
			}
			if adapter.ClassOf(err) == adapter.ErrUnauthorized {
				t.Fatalf("wrong class: %v", err)
			}
		})
	}
	f := &fakeTR{torrents: []byte(`{"result":"success","arguments":{"torrents":[]}}`), sid: "s"}
	snap, err := client(t, f, "", "").ListTransfers(context.Background())
	if err != nil || !snap.Complete || len(snap.Transfers) != 0 {
		t.Fatalf("explicit empty list must be a complete empty listing: %v %+v", err, snap)
	}
}

func TestMissingMetadataEvidenceIsUnknown(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int64) *int64 { return &v }
	status := func(v int) *int { return &v }
	pub := false
	base := func(meta *float64, st int) *torrent {
		return &torrent{HashString: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", Status: status(st), SizeWhenDone: i(100), LeftUntilDone: i(50),
			TotalSize: i(100), IsPrivate: &pub, MetadataPercent: meta, DesiredAvailable: i(50)}
	}
	for name, meta := range map[string]*float64{"absent/null": nil, "negative": f(-0.5), "above one": f(1.5)} {
		t.Run(name, func(t *testing.T) {
			x, err := normalize("tr", base(meta, 4))
			if err != nil {
				t.Fatal(err)
			}
			if x.Private.IsKnown() || x.VerifiedBytes.IsKnown() || x.WantedBytes.IsKnown() || x.Availability.IsKnown() || x.AllFilesWanted.IsKnown() {
				t.Fatalf("metadata-dependent evidence established without metadata evidence: %+v", x)
			}
			if x.StateClass == domain.StateComplete {
				t.Fatal("completion inferred without metadata evidence")
			}
			// Stopped with nothing left must not be read as complete either.
			b := base(meta, 0)
			b.LeftUntilDone = i(0)
			y, _ := normalize("tr", b)
			if y.StateClass == domain.StateComplete {
				t.Fatal("stopped torrent without metadata evidence treated as complete")
			}
		})
	}
	// Genuine known states.
	x, _ := normalize("tr", base(f(1), 4))
	if p, ok := x.Private.Get(); !ok || p || x.StateClass != domain.StateDownloading || x.VerifiedBytes.Value != 50 {
		t.Fatalf("metadata complete: %+v", x)
	}
	y, _ := normalize("tr", base(f(0.4), 4))
	if y.StateClass != domain.StateMetadata || y.Private.IsKnown() || y.VerifiedBytes.IsKnown() {
		t.Fatalf("metadata in progress: %+v", y)
	}
}

func TestInvalidMetadataTypeIsMalformed(t *testing.T) {
	body := `{"result":"success","arguments":{"torrents":[{"hashString":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":4,"metadataPercentComplete":"1"}]}}`
	f := &fakeTR{torrents: []byte(body), sid: "s"}
	if _, err := client(t, f, "", "").ListTransfers(context.Background()); err == nil {
		t.Fatal("a non-numeric metadataPercentComplete must be rejected")
	}
}
