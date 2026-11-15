package engine

import (
	"fmt"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Finding kinds per transfer, used as the last dedup-key segment.
const (
	kindStalled  = "stalled"
	kindMetadata = "metadata"
	kindError    = "error"
)

// Baseline keys recorded when a stall is established.
const (
	baseVerified = "verified_bytes"
	baseWanted   = "wanted_bytes"
)

// transferOutcome is the decision for one existing finding that does not
// currently re-qualify.
type transferOutcome struct {
	resolve  bool
	reason   string // resolution reason when resolve is true
	note     string // CurrentState explanation when carried forward
	baseline map[string]int64
}

// stallBaseline captures verified progress at diagnosis time.
func stallBaseline(t domain.Transfer) map[string]int64 {
	v, okV := t.VerifiedBytes.Get()
	w, okW := t.WantedBytes.Get()
	if !okV || !okW {
		return nil
	}
	return map[string]int64{baseVerified: v, baseWanted: w}
}

// decideStall determines what happens to an open stall finding when the
// transfer is listed but does not currently qualify. Only verified progress
// beyond the diagnosis baseline resolves it here; completion and
// disappearance are handled by the caller. A pause, queue wait, check,
// error, observation gap, clock anomaly, or missing evidence only changes
// the explanation.
func decideStall(f domain.Finding, t domain.Transfer, a policy.Assessment, s policy.Settings) transferOutcome {
	base := f.Detail.Baseline
	v, okV := t.VerifiedBytes.Get()
	w, okW := t.WantedBytes.Get()
	bv, hasV := base[baseVerified]
	bw, hasW := base[baseWanted]
	if okV && okW && hasV && hasW && w == bw && v-bv >= s.MinProgressBytes {
		return transferOutcome{resolve: true, reason: fmt.Sprintf(
			"Verified progress: %s more hash-checked data than when the stall was diagnosed (%s of %s wanted).",
			HumanBytes(v-bv), HumanBytes(v), HumanBytes(w))}
	}
	out := transferOutcome{baseline: base}
	const tail = " The stall is not treated as recovered; this finding resolves on verified progress, completion, or removal of the transfer."
	switch {
	case okV && okW && (!hasV || !hasW):
		out.baseline = stallBaseline(t)
		out.note = "No diagnosis baseline was recorded; the current verified data is now the baseline." + tail
	case okV && okW && w != bw:
		out.baseline = stallBaseline(t)
		out.note = fmt.Sprintf("File selection changed since diagnosis (wanted %s → %s); progress is now measured from the new selection.", HumanBytes(bw), HumanBytes(w)) + tail
	default:
		out.note = suspendedNote(t, a) + tail
	}
	return out
}

// suspendedNote describes why a stall or metadata condition is not
// currently being re-established. Durations are deliberately omitted so the
// note only changes when the situation does.
func suspendedNote(t domain.Transfer, a policy.Assessment) string {
	switch t.StateClass {
	case domain.StatePaused:
		return "The transfer is now paused in the client (by a user or scheduler)."
	case domain.StateQueued:
		return "The transfer is now waiting for an active slot in the client."
	case domain.StateChecking:
		return "The client is checking the data; evaluation continues afterwards from a new baseline."
	case domain.StateError:
		return "The client now reports an error for this transfer."
	case domain.StateMetadata:
		return "The client is fetching metadata again."
	case domain.StateTransient:
		return fmt.Sprintf("The client is busy (%s).", t.NativeState)
	case domain.StateUnknown:
		return fmt.Sprintf("The client reports an unrecognised state (%s).", t.NativeState)
	}
	switch a.Status {
	case policy.StatusStale:
		return "Current evidence is too old or inconsistent with the system clock to re-evaluate."
	case policy.StatusNoEvidence:
		return "Verified progress is currently unavailable from the client."
	}
	if n := len(a.Resets); n > 0 {
		return "Qualification restarted: " + a.Resets[n-1].Reason + "."
	}
	return "Evidence is still accumulating after a restart of qualification."
}

// decideMetadata resolves a metadata-timeout finding only once the client
// reports metadata (a known wanted size).
func decideMetadata(t domain.Transfer, a policy.Assessment) transferOutcome {
	if w, ok := t.WantedBytes.Get(); ok && t.StateClass != domain.StateMetadata {
		return transferOutcome{resolve: true, reason: fmt.Sprintf("Verified: metadata acquired (wanted size %s; client state %s).", HumanBytes(w), t.NativeState)}
	}
	return transferOutcome{note: suspendedNote(t, a) + " Metadata has not been acquired; this finding resolves when it is, or when the transfer completes or is removed."}
}

// decideError resolves a client-error finding after two consecutive
// non-error observations.
func decideError(t domain.Transfer, obs []store.Observation) transferOutcome {
	if consecutiveNonErrors(obs) >= 2 {
		return transferOutcome{resolve: true, reason: fmt.Sprintf("Verified: the client has reported a non-error state on consecutive observations (now %s).", t.NativeState)}
	}
	if t.StateClass == domain.StateError {
		return transferOutcome{note: "The client still reports an error."}
	}
	return transferOutcome{note: fmt.Sprintf("The client now reports %s; awaiting a second consecutive non-error observation before resolving.", t.NativeState)}
}

func consecutiveNonErrors(obs []store.Observation) int {
	n := 0
	for i := len(obs) - 1; i >= 0 && obs[i].StateClass != domain.StateError; i-- {
		n++
	}
	return n
}
