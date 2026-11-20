package engine

import (
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Finding kinds per transfer, used as the last dedup-key segment.
const (
	kindStalled  = "stalled"
	kindMetadata = "metadata"
	kindError    = "error"
	kindSource   = "source"
	// kindClientRefused and kindCompleteBlocked are diagnoses that also
	// apply to complete transfers.
	kindClientRefused   = "client_refused"
	kindCompleteBlocked = "complete_blocked"
)

// Baseline keys recorded when a stall is established.
const (
	baseVerified = "verified_bytes"
	baseWanted   = "wanted_bytes"
	// baseRecheck is set while a data check observed after diagnosis has not
	// yet been followed by a downloading observation. Verified counters
	// reported during or after a check are not comparable with the earlier
	// baseline, so the first downloading observation afterwards becomes the
	// new baseline instead of counting as progress.
	baseRecheck = "recheck_pending"
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

func withRecheck(base map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out[baseRecheck] = 1
	return out
}

// decideStall determines what happens to an open stall finding when the
// transfer is listed but does not currently qualify. Only verified progress
// beyond the applicable baseline, observed while the client is actively
// downloading and the evidence is fresh, resolves it here; completion and
// disappearance are handled by the caller. A pause, queue wait, check,
// error, observation gap, clock anomaly, or missing evidence only changes
// the explanation. A check establishes a new baseline: counters that rise
// during or because of a recheck are not download progress.
func decideStall(f domain.Finding, t domain.Transfer, a policy.Assessment, s policy.Settings) transferOutcome {
	base := f.Detail.Baseline
	const tail = " The stall is not treated as recovered; this finding resolves on verified progress, completion, or removal of the transfer."
	if t.StateClass == domain.StateChecking {
		return transferOutcome{baseline: withRecheck(base), note: suspendedNote(t, a) + tail}
	}
	v, okV := t.VerifiedBytes.Get()
	w, okW := t.WantedBytes.Get()
	bv, hasV := base[baseVerified]
	bw, hasW := base[baseWanted]
	evaluable := t.StateClass == domain.StateDownloading && okV && okW && a.Status != policy.StatusStale
	if evaluable && base[baseRecheck] != 0 {
		return transferOutcome{baseline: stallBaseline(t), note: fmt.Sprintf(
			"The client finished checking the data; progress is now measured from the %s of %s verified after the check.",
			HumanBytes(v), HumanBytes(w)) + tail}
	}
	if evaluable && hasV && hasW && w == bw && v-bv >= s.MinProgressBytes {
		return transferOutcome{resolve: true, reason: fmt.Sprintf(
			"Verified progress: %s more hash-checked data than when the stall was diagnosed (%s of %s wanted).",
			HumanBytes(v-bv), HumanBytes(v), HumanBytes(w))}
	}
	out := transferOutcome{baseline: base}
	switch {
	case evaluable && (!hasV || !hasW):
		out.baseline = stallBaseline(t)
		out.note = "No diagnosis baseline was recorded; the current verified data is now the baseline." + tail
	case evaluable && w != bw:
		out.baseline = stallBaseline(t)
		out.note = fmt.Sprintf("File selection changed since diagnosis (wanted %s → %s); progress is now measured from the new selection.", HumanBytes(bw), HumanBytes(w)) + tail
	case base[baseRecheck] != 0:
		out.note = suspendedNote(t, a) + " A data check was observed; progress will be measured from the first active observation after it." + tail
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

// decideError resolves a client-error finding only on positive evidence of
// clearance: the two latest observations both report a recognised non-error
// state, are fresh, and are not separated by a gap or clock anomaly. An
// unrecognised or unknown state is not evidence that the error cleared.
func decideError(t domain.Transfer, obs []store.Observation, a policy.Assessment, s policy.Settings) transferOutcome {
	if a.Status != policy.StatusStale && consecutiveCleared(obs, s.MaxObservationGap) >= 2 {
		return transferOutcome{resolve: true, reason: fmt.Sprintf("Verified: the client has reported a recognised non-error state on consecutive observations (now %s).", t.NativeState)}
	}
	switch {
	case t.StateClass == domain.StateError:
		return transferOutcome{note: "The client still reports an error."}
	case t.StateClass == domain.StateUnknown:
		return transferOutcome{note: fmt.Sprintf("The client now reports an unrecognised state (%s), which does not establish that the error cleared; this finding stays open until a recognised non-error state is observed twice in a row.", t.NativeState)}
	case a.Status == policy.StatusStale:
		return transferOutcome{note: fmt.Sprintf("The client now reports %s, but the evidence is too old or inconsistent with the system clock to verify recovery.", t.NativeState)}
	}
	return transferOutcome{note: fmt.Sprintf("The client now reports %s; awaiting a second consecutive non-error observation before resolving.", t.NativeState)}
}

// clearsError reports whether a state is positive evidence that the client
// is not in an error state.
func clearsError(c domain.StateClass) bool {
	switch c {
	case domain.StateQueued, domain.StatePaused, domain.StateChecking, domain.StateMetadata,
		domain.StateDownloading, domain.StateComplete, domain.StateTransient:
		return true
	}
	return false
}

// consecutiveCleared counts the latest run of error-clearing observations
// with no gap larger than maxGap and strictly increasing timestamps.
func consecutiveCleared(obs []store.Observation, maxGap time.Duration) int {
	n := 0
	for i := len(obs) - 1; i >= 0 && clearsError(obs[i].StateClass); i-- {
		if i < len(obs)-1 {
			d := obs[i+1].At.Sub(obs[i].At)
			if d <= 0 || d > maxGap {
				break
			}
		}
		n++
	}
	return n
}

// decideSource resolves a source-failure finding only on positive evidence
// that the tracker no longer rejects the transfer: the two latest
// observations both report a known, different tracker result, are fresh,
// and are not separated by a gap. A paused transfer's stale message or an
// unknown result proves nothing either way.
func decideSource(t domain.Transfer, obs []store.Observation, a policy.Assessment, s policy.Settings) transferOutcome {
	n := 0
	for i := len(obs) - 1; i >= 0; i-- {
		v, ok := obs[i].SourceFailure.Get()
		if !ok || v == domain.SourceUnregistered {
			break
		}
		if i < len(obs)-1 && (obs[i+1].At.Sub(obs[i].At) > s.MaxObservationGap || !obs[i+1].At.After(obs[i].At)) {
			break
		}
		n++
	}
	if a.Status != policy.StatusStale && n >= 2 {
		return transferOutcome{resolve: true, reason: "Verified: the client has reported no unregistered rejection from the tracker on consecutive observations."}
	}
	if v, ok := t.SourceFailure.Get(); ok && v == domain.SourceUnregistered {
		return transferOutcome{note: suspendedNote(t, a) + " The tracker still rejects this torrent as not registered."}
	}
	return transferOutcome{note: suspendedNote(t, a) + " The tracker rejection is no longer reported in the latest observation (" +
		t.SourceFailure.String() + "); this finding resolves after two consecutive observations without it."}
}
