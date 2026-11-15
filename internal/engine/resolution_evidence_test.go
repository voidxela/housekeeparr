package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// R2: a counter increase reported while (or because) the client checks data
// is not download progress; the check establishes a new baseline.
func TestRecheckCounterIncreaseDoesNotResolveStall(t *testing.T) {
	h := stalledSetup(t)
	before := h.mustOneActive(domain.FindingTransferStalled)
	h.client.Update(hash, func(x *domain.Transfer) {
		x.StateClass, x.NativeState, x.VerifiedBytes = domain.StateChecking, "checkingDL", domain.KnownValue(3*gib)
	})
	h.run(20, 10)
	f := h.mustOneActive(domain.FindingTransferStalled)
	if f.ID != before.ID || !strings.Contains(f.Detail.CurrentState, "checking") {
		t.Fatalf("checking: %d %q", f.ID, f.Detail.CurrentState)
	}
	if f.Detail.Baseline[baseVerified] != 2*gib || f.Detail.Baseline[baseRecheck] == 0 {
		t.Fatalf("baseline during check: %+v", f.Detail.Baseline)
	}

	// Resumed downloading with the post-check counter: new baseline, no resolution.
	h.client.Update(hash, func(x *domain.Transfer) { x.StateClass, x.NativeState = domain.StateDownloading, "stalledDL" })
	h.run(10, 10)
	f = h.mustOneActive(domain.FindingTransferStalled)
	if f.Detail.Baseline[baseVerified] != 3*gib || f.Detail.Baseline[baseRecheck] != 0 {
		t.Fatalf("post-check baseline: %+v", f.Detail.Baseline)
	}
	if !strings.Contains(f.Detail.CurrentState, "finished checking") {
		t.Fatalf("note %q", f.Detail.CurrentState)
	}
	h.run(20, 10)
	h.mustOneActive(domain.FindingTransferStalled)

	// Genuine progress beyond the post-check baseline resolves.
	h.client.Update(hash, func(x *domain.Transfer) { x.VerifiedBytes = domain.KnownValue(3*gib + 4<<20) })
	h.run(10, 10)
	if len(h.active(domain.FindingTransferStalled)) != 0 {
		t.Fatal("progress beyond the post-check baseline must resolve")
	}
	if r := h.all(domain.FindingTransferStalled)[0].Resolution; !strings.HasPrefix(r, "Verified progress") {
		t.Fatalf("resolution %q", r)
	}
}

func TestRecheckBaselineSurvivesInterveningSuspension(t *testing.T) {
	h := stalledSetup(t)
	h.client.Update(hash, func(x *domain.Transfer) { x.StateClass, x.NativeState = domain.StateChecking, "checkingDL" })
	h.run(10, 10)
	// Paused after the check with a higher counter: still not progress.
	h.client.Update(hash, func(x *domain.Transfer) {
		x.StateClass, x.NativeState, x.VerifiedBytes = domain.StatePaused, "stoppedDL", domain.KnownValue(5*gib)
	})
	h.run(20, 10)
	f := h.mustOneActive(domain.FindingTransferStalled)
	if f.Detail.Baseline[baseRecheck] == 0 || !strings.Contains(f.Detail.CurrentState, "data check was observed") {
		t.Fatalf("%+v %q", f.Detail.Baseline, f.Detail.CurrentState)
	}
	h.client.Update(hash, func(x *domain.Transfer) { x.StateClass, x.NativeState = domain.StateDownloading, "stalledDL" })
	h.run(10, 10)
	if f := h.mustOneActive(domain.FindingTransferStalled); f.Detail.Baseline[baseVerified] != 5*gib {
		t.Fatalf("baseline %+v", f.Detail.Baseline)
	}
}

func TestNonDownloadingCounterIncreaseDoesNotResolveStall(t *testing.T) {
	for _, st := range []struct {
		class  domain.StateClass
		native string
	}{{domain.StateUnknown, "futureState"}, {domain.StateQueued, "queuedDL"}, {domain.StateError, "error"}} {
		t.Run(string(st.class), func(t *testing.T) {
			h := stalledSetup(t)
			h.client.Update(hash, func(x *domain.Transfer) {
				x.StateClass, x.NativeState, x.VerifiedBytes = st.class, st.native, domain.KnownValue(4*gib)
			})
			h.run(20, 10)
			h.mustOneActive(domain.FindingTransferStalled)
		})
	}
}

// R3: unknown states are not evidence that a client error cleared.
func TestUnknownStateDoesNotResolveClientError(t *testing.T) {
	h := stalledSetup(t)
	h.client.Update(hash, func(x *domain.Transfer) { x.StateClass, x.NativeState = domain.StateError, "error" })
	h.run(20, 10)
	before := h.mustOneActive(domain.FindingTransferError)
	h.client.Update(hash, func(x *domain.Transfer) { x.StateClass, x.NativeState = domain.StateUnknown, "futureState" })
	h.run(30, 10)
	f := h.mustOneActive(domain.FindingTransferError)
	if f.ID != before.ID || !strings.Contains(f.Detail.CurrentState, "unrecognised state (futureState)") {
		t.Fatalf("%d %q", f.ID, f.Detail.CurrentState)
	}
	// One recognised state after unknowns is not yet two consecutive.
	h.client.Update(hash, func(x *domain.Transfer) { x.StateClass, x.NativeState = domain.StateDownloading, "stalledDL" })
	h.run(10, 10)
	h.mustOneActive(domain.FindingTransferError)
	h.run(10, 10)
	if len(h.active(domain.FindingTransferError)) != 0 {
		t.Fatal("two consecutive recognised non-error observations must resolve")
	}
	if r := h.all(domain.FindingTransferError)[0].Resolution; !strings.Contains(r, "recognised non-error state") {
		t.Fatalf("resolution %q", r)
	}
}

func TestErrorClearanceAcrossObservationGapIsNotConsecutive(t *testing.T) {
	h := stalledSetup(t)
	h.client.Update(hash, func(x *domain.Transfer) { x.StateClass, x.NativeState = domain.StateError, "error" })
	h.run(20, 10)
	h.mustOneActive(domain.FindingTransferError)
	h.client.Update(hash, func(x *domain.Transfer) { x.StateClass, x.NativeState = domain.StatePaused, "stoppedDL" })
	h.run(10, 10)
	h.clock.Advance(45 * time.Minute) // exceeds the 20 min maximum gap
	h.cycle()
	h.mustOneActive(domain.FindingTransferError)
	h.run(10, 10)
	if len(h.active(domain.FindingTransferError)) != 0 {
		t.Fatal("two fresh consecutive observations after the gap must resolve")
	}
}
