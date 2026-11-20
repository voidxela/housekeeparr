package engine

import (
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/fake"
	"github.com/voidxela/housekeeparr/internal/store"
)

func activeOf(t *testing.T, h *harness, typ string) []domain.Finding {
	t.Helper()
	fs, err := h.st.Findings(ctx, store.FindingFilter{Type: typ, States: []domain.FindingState{domain.FindingOpen}})
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// A tracker refusing the client (seen: "blacklisted client, update or
// change client" with Transmission 4.0.5) is explained as such, never as a
// stall or a dead release, on an active download (two consecutive
// observations) and on a complete one (the latest listing). It resolves
// when the tracker accepts the client again.
func TestTrackerRefusingTheClientIsExplained(t *testing.T) {
	h := newHarness(t, baseConfig)
	refuse := func(tr *domain.Transfer) {
		tr.SourceFailure = domain.KnownValue(domain.SourceClientRejected)
		tr.ClientMessage = domain.KnownValue("blacklisted client, update or change client")
	}
	tr := fake.Transfer(hash, "Show.S01E01", domain.StateDownloading, 1*gib, 4*gib, t0)
	refuse(&tr)
	h.client.SetTransfers(tr)
	h.cycle()
	if fs := activeOf(t, h, domain.FindingTrackerRejectsClient); len(fs) != 0 {
		t.Fatalf("drafted on one observation: %+v", fs)
	}
	// Three hours without progress: no stall, a client refusal.
	h.run(180, 10)
	fs := activeOf(t, h, domain.FindingTrackerRejectsClient)
	if len(fs) != 1 || !strings.Contains(fs[0].Summary, "not a dead release") || !strings.Contains(strings.Join(fs[0].Detail.NextSteps, " "), "Upgrade") {
		t.Fatalf("refusal finding %+v", fs)
	}
	if st := activeOf(t, h, domain.FindingTransferStalled); len(st) != 0 {
		t.Fatalf("a client refusal was diagnosed as a stall: %+v", st)
	}
	// The tracker accepts an upgraded client.
	h.client.Update(hash, func(tr *domain.Transfer) { tr.SourceFailure = domain.KnownValue(domain.SourceNoFailure) })
	h.run(10, 10)
	if fs := activeOf(t, h, domain.FindingTrackerRejectsClient); len(fs) != 0 {
		t.Fatalf("not resolved: %+v", fs)
	}
	// A complete torrent refused by its tracker (seeding is affected).
	done := fake.Transfer("cccccccccccccccccccccccccccccccccccccccc", "Show.S01E02", domain.StateComplete, 4*gib, 4*gib, t0)
	refuse(&done)
	h.client.SetTransfers(done)
	h.run(10, 10)
	if fs := activeOf(t, h, domain.FindingTrackerRejectsClient); len(fs) != 1 || !strings.Contains(fs[0].Detail.Confidence, "latest listing") {
		t.Fatalf("complete refused torrent: %+v", fs)
	}
}

// A complete payload that the client holds in an error, while the manager
// still tracks the grab as downloading, is explained; it is not a stall and
// it resolves once the manager no longer tracks it.
func TestCompletePayloadNotImported(t *testing.T) {
	h := newHarness(t, baseConfig)
	tr := fake.Transfer(hash, "Show.S01E01", domain.StateError, 4*gib, 4*gib, t0)
	tr.NativeState, tr.ClientMessage = "error", domain.KnownValue("blacklisted client, update or change client")
	h.client.SetTransfers(tr)
	h.mgr.SetQueue(fake.Episode(1, hash, "Show.S01E01", "Example Show", 1, 1))
	h.run(20, 10)
	fs := activeOf(t, h, domain.FindingCompleteNotImported)
	if len(fs) != 1 || !strings.Contains(fs[0].Summary, "Every wanted byte is verified") || !strings.Contains(strings.Join(fs[0].Detail.NextSteps, " "), "import the files manually") {
		t.Fatalf("finding %+v", fs)
	}
	// A partly verified errored download is not "complete".
	h.client.Update(hash, func(tr *domain.Transfer) { tr.VerifiedBytes = domain.KnownValue(3 * gib) })
	h.run(10, 10)
	if fs := activeOf(t, h, domain.FindingCompleteNotImported); len(fs) != 0 {
		t.Fatalf("partly verified payload still reported complete: %+v", fs)
	}
	h.client.Update(hash, func(tr *domain.Transfer) { tr.VerifiedBytes = domain.KnownValue(4 * gib) })
	h.run(10, 10)
	if len(activeOf(t, h, domain.FindingCompleteNotImported)) != 1 {
		t.Fatal("not reported again")
	}
	// Imported (the manager no longer tracks it).
	h.mgr.SetQueue()
	h.run(10, 10)
	if fs := activeOf(t, h, domain.FindingCompleteNotImported); len(fs) != 0 {
		t.Fatalf("not resolved after the queue item left: %+v", fs)
	}
}

// A settled import problem gets its download's files listed, a bounded
// number per cycle and once per candidate; archives with no video are
// explained as needing extraction.
func TestImportFindingNamesArchiveOnlyPayload(t *testing.T) {
	h := newHarness(t, baseConfig)
	var hashes []string
	var rows []domain.QueueItem
	var trs []domain.Transfer
	for i := 0; i < 5; i++ {
		hs := strings.Repeat(string(rune('a'+i)), 40)
		hashes = append(hashes, hs)
		trs = append(trs, fake.Transfer(hs, "Show.S01E0"+string(rune('1'+i)), domain.StateComplete, 4*gib, 4*gib, t0))
		q := fake.Episode(int64(i+1), hs, "Show.S01E0"+string(rune('1'+i)), "Example Show", 1, int64(i+1))
		q.TrackedState, q.TrackedStatus = domain.KnownValue("importBlocked"), domain.KnownValue("warning")
		q.StatusMessages = []domain.StatusMessage{{Title: "Show.S01E01", Messages: []string{"No files found are eligible for import"}}}
		rows = append(rows, q)
		h.client.SetFiles(hs, domain.TransferFile{Path: "Show/show.rar", Size: 1, Wanted: domain.KnownValue(true)},
			domain.TransferFile{Path: "Show/show.r00", Size: 1, Wanted: domain.KnownValue(true)})
	}
	h.client.SetTransfers(trs...)
	h.mgr.SetQueue(rows...)
	h.run(40, 10) // past the 30-minute import settling period
	if n := h.client.FileReads(); n == 0 || n > payloadReadsPerCycle*2 {
		t.Fatalf("file reads after settling: %d", n)
	}
	h.run(30, 10)
	if n := h.client.FileReads(); n != len(hashes) {
		t.Fatalf("each candidate read once: %d reads for %d", n, len(hashes))
	}
	fs := activeOf(t, h, domain.FindingImportAttention)
	if len(fs) != len(hashes) {
		t.Fatalf("%d import findings", len(fs))
	}
	for _, f := range fs {
		if !strings.Contains(f.Summary, "needs extracting") || !strings.Contains(f.Detail.NextSteps[0], "never extracts") {
			t.Fatalf("finding %q steps %v", f.Summary, f.Detail.NextSteps)
		}
	}
}
