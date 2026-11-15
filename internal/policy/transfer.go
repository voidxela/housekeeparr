package policy

import (
	"fmt"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

// Sample is one successful observation of a transfer. Failed reads produce
// no sample; the resulting gap is visible to the evaluator.
type Sample struct {
	At time.Time
	// RunID and MonoMS identify the process and its monotonic clock reading
	// (milliseconds since process start). Within one run, wall-clock and
	// monotonic deltas are compared to detect clock jumps.
	RunID  string
	MonoMS int64

	StateClass     domain.StateClass
	NativeState    string
	WantedBytes    domain.Signal[int64]
	VerifiedBytes  domain.Signal[int64]
	Availability   domain.Signal[float64]
	AllFilesWanted domain.Signal[bool]
	// AvailabilityWantedOnly marks availability figures that already cover
	// only wanted data; file selection then cannot make them non-specific.
	AvailabilityWantedOnly bool
}

// TransferInput is everything the evaluator needs for one transfer.
type TransferInput struct {
	AddedAt   domain.Signal[time.Time]
	FirstSeen time.Time
	// Samples are ordered by At (as persisted) and should cover at least the
	// longest applicable window plus grace.
	Samples []Sample
	// AvailabilityMeaning describes the client's availability figure.
	AvailabilityMeaning string
}

// Status is the evaluator's overall verdict for a transfer.
type Status string

const (
	StatusNotApplicable Status = "not_applicable" // complete, paused, queued, checking, error...
	StatusNoEvidence    Status = "no_evidence"    // no usable observations
	StatusStale         Status = "stale"          // latest observation too old to evaluate
	StatusGrace         Status = "grace"          // within new-download grace
	StatusObserving     Status = "observing"      // accumulating qualifying evidence
	StatusProgressing   Status = "progressing"    // recent verified progress
	StatusQualified     Status = "qualified"      // a diagnosis is established
)

// Diagnosis is the established condition when Status is qualified.
type Diagnosis string

const (
	DiagnosisNone Diagnosis = ""
	// DiagnosisNoProgress: sustained no meaningful verified progress; piece
	// availability unknown or not shown to be lacking.
	DiagnosisNoProgress Diagnosis = "no_progress"
	// DiagnosisNoProgressUnavailable: as above, with positive evidence that
	// required pieces are missing among connected peers for the whole window.
	DiagnosisNoProgressUnavailable Diagnosis = "no_progress_pieces_unavailable"
	// DiagnosisMetadataTimeout: metadata acquisition exceeded its timeout.
	DiagnosisMetadataTimeout Diagnosis = "metadata_timeout"
)

// AvailabilityEvidence summarises piece availability over the qualifying
// window. Unknown never becomes "unavailable".
type AvailabilityEvidence string

const (
	AvailUnknown      AvailabilityEvidence = "unknown"
	AvailNotSpecific  AvailabilityEvidence = "not_specific_to_wanted_data"
	AvailLacking      AvailabilityEvidence = "connected_peers_lack_pieces"
	AvailSeenComplete AvailabilityEvidence = "available_among_connected_peers"
)

// Reset records why qualification restarted.
type Reset struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason"`
}

// Assessment is the evaluator's explained result.
type Assessment struct {
	Status    Status    `json:"status"`
	Diagnosis Diagnosis `json:"diagnosis,omitempty"`
	Headline  string    `json:"headline"`
	Details   []string  `json:"details,omitempty"`

	EvidenceAt     time.Time            `json:"evidence_at"`
	GraceEnds      time.Time            `json:"grace_ends"`
	GraceFrom      string               `json:"grace_from"` // "client added time" or "first observation"
	StreakStart    time.Time            `json:"streak_start,omitempty"`
	Observations   int                  `json:"observations"`     // qualifying observations after grace
	Elapsed        time.Duration        `json:"elapsed"`          // qualifying time counted
	Window         time.Duration        `json:"window,omitempty"` // required qualifying time
	WindowReason   string               `json:"window_reason,omitempty"`
	QualifiesAt    *time.Time           `json:"qualifies_at,omitempty"` // estimate if unchanged
	LastProgressAt *time.Time           `json:"last_progress_at,omitempty"`
	Availability   AvailabilityEvidence `json:"availability"`
	Resets         []Reset              `json:"resets,omitempty"`
	Limitations    []string             `json:"limitations,omitempty"`
	PolicyRevision string               `json:"policy_revision"`
}

const maxResets = 5

// EvaluateTransfer applies the no-progress and metadata diagnoses.
//
// Definitions (documented for operators in docs/policy.md):
//   - Grace ends NewDownloadGrace after the client's added time when known,
//     otherwise after Housekeeparr's first observation.
//   - A streak is a run of consecutive observations in the same evaluable
//     state (downloading, or metadata) with no baseline change. Gaps larger
//     than MaxObservationGap, clock anomalies, state changes, file-selection
//     changes, verified-byte decreases, and unknown progress restart it.
//   - Within a streak, each interval contributes at most PollInterval of
//     qualifying time, so a missed sample never counts as failure evidence,
//     and only time after grace counts.
//   - Verified progress of at least MinProgressBytes relative to the streak
//     baseline moves the baseline; slow sustained progress therefore never
//     qualifies, while network traffic without verified data does not help.
//   - Qualification requires both MinObservations observations after grace
//     and Window of qualifying time.
func EvaluateTransfer(in TransferInput, s Settings, now time.Time) Assessment {
	a := Assessment{PolicyRevision: s.Revision(), Availability: AvailUnknown}
	if in.AddedAt.IsKnown() {
		a.GraceEnds = in.AddedAt.Value.Add(s.NewDownloadGrace)
		a.GraceFrom = "client added time"
	} else {
		a.GraceEnds = in.FirstSeen.Add(s.NewDownloadGrace)
		a.GraceFrom = "first observation (client added time unavailable)"
		a.Limitations = append(a.Limitations, "The client did not report when this download was added; grace is measured from Housekeeparr's first observation.")
	}
	if len(in.Samples) == 0 {
		a.Status = StatusNoEvidence
		a.Headline = "No successful observations yet."
		return a
	}
	last := in.Samples[len(in.Samples)-1]
	a.EvidenceAt = last.At

	if now.Before(last.At.Add(-s.ClockTolerance)) {
		a.Status = StatusStale
		a.Headline = "System clock is earlier than the latest evidence; waiting for renewed observations."
		a.Limitations = append(a.Limitations, fmt.Sprintf("Latest observation is stamped %s, which is after the current time.", last.At.UTC().Format(time.RFC3339)))
		return a
	}
	if age := now.Sub(last.At); age > s.MaxObservationGap {
		a.Status = StatusStale
		a.Headline = fmt.Sprintf("Evidence is %s old; diagnosis waits for fresh observations.", roundDur(age))
		return a
	}

	switch last.StateClass {
	case domain.StateDownloading:
		evaluateNoProgress(&a, in, s, now)
	case domain.StateMetadata:
		evaluateMetadata(&a, in, s, now)
	default:
		a.Status = StatusNotApplicable
		a.Headline = notApplicableHeadline(last)
	}
	return a
}

func notApplicableHeadline(last Sample) string {
	switch last.StateClass {
	case domain.StateComplete:
		return "Download complete; no progress diagnosis applies."
	case domain.StatePaused:
		return "Paused in the client (user or scheduler); not treated as a stall."
	case domain.StateQueued:
		return "Waiting for an active slot in the client; not treated as a stall."
	case domain.StateChecking:
		return "The client is checking data; evaluation resumes afterwards with a new baseline."
	case domain.StateError:
		return "The client reports an error for this download."
	case domain.StateTransient:
		return fmt.Sprintf("The client is busy (%s); evaluation resumes afterwards.", last.NativeState)
	default:
		return fmt.Sprintf("Unrecognised client state %q; not evaluated.", last.NativeState)
	}
}

// breakReason returns why the streak cannot continue from prev to cur.
func breakReason(prev, cur Sample, s Settings) string {
	d := cur.At.Sub(prev.At)
	switch {
	case d <= 0:
		return "clock moved backward or repeated a timestamp between observations"
	case d > s.MaxObservationGap:
		return fmt.Sprintf("observation gap of %s exceeded the %s maximum", roundDur(d), roundDur(s.MaxObservationGap))
	}
	if prev.RunID != "" && prev.RunID == cur.RunID {
		mono := time.Duration(cur.MonoMS-prev.MonoMS) * time.Millisecond
		if diff := d - mono; diff > s.ClockTolerance || diff < -s.ClockTolerance {
			return fmt.Sprintf("wall clock jumped by %s relative to elapsed process time", roundDur(diff))
		}
	}
	if cur.StateClass != prev.StateClass {
		return fmt.Sprintf("state changed from %s to %s", prev.StateClass, cur.StateClass)
	}
	if cur.StateClass == domain.StateMetadata {
		return ""
	}
	pw, okp := prev.WantedBytes.Get()
	cw, okc := cur.WantedBytes.Get()
	if !okp || !okc {
		return "wanted size unknown"
	}
	if pw != cw {
		return "file selection or wanted size changed"
	}
	pv, okp := prev.VerifiedBytes.Get()
	cv, okc := cur.VerifiedBytes.Get()
	if !okp || !okc {
		return "verified progress unknown"
	}
	if cv < pv {
		return "verified data decreased (recheck, client reset, or data loss)"
	}
	return ""
}

// streak walks samples forward and returns the index where the current
// streak begins, recording resets and the last verified progress.
func streak(a *Assessment, samples []Sample, s Settings, class domain.StateClass) int {
	start := -1
	for i, cur := range samples {
		if cur.StateClass != class {
			if start >= 0 {
				addReset(a, cur.At, fmt.Sprintf("state changed to %s", cur.StateClass))
			}
			start = -1
			continue
		}
		if class == domain.StateDownloading && (!cur.VerifiedBytes.IsKnown() || !cur.WantedBytes.IsKnown()) {
			if start >= 0 {
				addReset(a, cur.At, "verified progress unknown")
			}
			start = -1
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		if r := breakReason(samples[i-1], cur, s); r != "" {
			addReset(a, cur.At, r)
			start = i
			continue
		}
		if class == domain.StateDownloading {
			base := samples[start].VerifiedBytes.Value
			if cur.VerifiedBytes.Value-base >= s.MinProgressBytes {
				t := cur.At
				a.LastProgressAt = &t
				start = i
			}
		}
	}
	return start
}

func addReset(a *Assessment, at time.Time, reason string) {
	a.Resets = append(a.Resets, Reset{At: at, Reason: reason})
	if len(a.Resets) > maxResets {
		a.Resets = a.Resets[len(a.Resets)-maxResets:]
	}
}

// qualifying counts observations and elapsed time after grace in a streak.
func qualifying(samples []Sample, graceEnds time.Time, poll time.Duration) (int, time.Duration) {
	n := 0
	var elapsed time.Duration
	for i, cur := range samples {
		if !cur.At.Before(graceEnds) {
			n++
		}
		if i == 0 || !cur.At.After(graceEnds) {
			continue
		}
		from := samples[i-1].At
		if from.Before(graceEnds) {
			from = graceEnds
		}
		d := cur.At.Sub(from)
		if d > poll {
			d = poll
		}
		elapsed += d
	}
	return n, elapsed
}

// estimate returns when qualification could first be observed, assuming
// conditions remain unchanged and samples arrive every poll interval.
func estimate(last time.Time, graceEnds time.Time, n int, elapsed, window time.Duration, s Settings) time.Time {
	needTime := window - elapsed
	at := last.Add(needTime)
	if last.Before(graceEnds) {
		at = graceEnds.Add(window)
	}
	if needObs := s.MinObservations - n; needObs > 0 {
		if t := last.Add(time.Duration(needObs) * s.PollInterval); t.After(at) {
			at = t
		}
	}
	// Qualification is only observed at a poll: round up to the next one.
	if at.After(last) {
		steps := (at.Sub(last) + s.PollInterval - 1) / s.PollInterval
		at = last.Add(steps * s.PollInterval)
	}
	return at
}

func evaluateNoProgress(a *Assessment, in TransferInput, s Settings, now time.Time) {
	samples := in.Samples
	start := streak(a, samples, s, domain.StateDownloading)
	last := samples[len(samples)-1]
	if start < 0 {
		a.Status = StatusNoEvidence
		a.Headline = "Verified progress is not available from this client; the no-progress diagnosis cannot be evaluated."
		a.Limitations = append(a.Limitations, "Network traffic alone is not used as a progress measure.")
		return
	}
	run := samples[start:]
	a.StreakStart = run[0].At

	window, reason := s.NoProgressWindow, "standard no-progress window"
	if w, v := last.WantedBytes.Value, last.VerifiedBytes.Value; w > 0 && float64(v)/float64(w) >= s.NearCompleteRatio {
		window = s.NearCompleteWindow
		reason = fmt.Sprintf("extended window: at least %.0f%% complete", s.NearCompleteRatio*100)
	}
	a.Window, a.WindowReason = window, reason
	a.Observations, a.Elapsed = qualifying(run, a.GraceEnds, s.PollInterval)
	a.Availability = availabilityEvidence(run, a.GraceEnds)
	switch a.Availability {
	case AvailUnknown:
		a.Limitations = append(a.Limitations, "Piece availability is unknown for part or all of the window; unknown availability is not treated as unavailable.")
	case AvailNotSpecific:
		a.Limitations = append(a.Limitations, "Some files are deselected, so the client's availability figure is not specific to wanted data.")
	}
	if in.AvailabilityMeaning != "" && (a.Availability == AvailLacking || a.Availability == AvailSeenComplete) {
		a.Limitations = append(a.Limitations, in.AvailabilityMeaning)
	}

	sinceProgress := "since observation began"
	if a.LastProgressAt != nil {
		sinceProgress = "since the last verified progress"
	}
	noProgressFor := last.At.Sub(run[0].At)
	if last.At.Before(a.GraceEnds) {
		a.Status = StatusGrace
		a.Headline = fmt.Sprintf("New download grace period ends in %s.", roundDur(a.GraceEnds.Sub(now)))
		q := estimate(last.At, a.GraceEnds, a.Observations, a.Elapsed, window, s)
		a.QualifiesAt = &q
		return
	}
	if a.Observations >= s.MinObservations && a.Elapsed >= window {
		a.Status = StatusQualified
		a.Diagnosis = DiagnosisNoProgress
		if a.Availability == AvailLacking {
			a.Diagnosis = DiagnosisNoProgressUnavailable
		}
		a.Headline = fmt.Sprintf("No verified progress for %s %s (%d qualifying observations, %s required).",
			roundDur(noProgressFor), sinceProgress, a.Observations, roundDur(window))
		return
	}
	if a.LastProgressAt != nil && !a.LastProgressAt.Before(last.At) {
		a.Status = StatusProgressing
		a.Headline = "Verified progress in the latest observation."
		return
	}
	a.Status = StatusObserving
	q := estimate(last.At, a.GraceEnds, a.Observations, a.Elapsed, window, s)
	a.QualifiesAt = &q
	a.Headline = fmt.Sprintf("No verified progress for %s %s. The no-progress diagnosis would be established in about %s if conditions remain unchanged.",
		roundDur(noProgressFor), sinceProgress, roundDur(q.Sub(now)))
	a.Details = append(a.Details, fmt.Sprintf("%d of %d required observations; %s of %s qualifying time counted.",
		a.Observations, s.MinObservations, roundDur(a.Elapsed), roundDur(window)))
}

func availabilityEvidence(run []Sample, graceEnds time.Time) AvailabilityEvidence {
	counted := 0
	lacking := true
	for _, smp := range run {
		if smp.At.Before(graceEnds) {
			continue
		}
		counted++
		if !smp.AvailabilityWantedOnly {
			all, ok := smp.AllFilesWanted.Get()
			if !ok {
				return AvailUnknown
			}
			if !all {
				return AvailNotSpecific
			}
		}
		v, ok := smp.Availability.Get()
		if !ok || v < 0 {
			return AvailUnknown
		}
		if v >= 1 {
			lacking = false
		}
	}
	if counted == 0 {
		return AvailUnknown
	}
	if lacking {
		return AvailLacking
	}
	return AvailSeenComplete
}

func evaluateMetadata(a *Assessment, in TransferInput, s Settings, now time.Time) {
	samples := in.Samples
	start := streak(a, samples, s, domain.StateMetadata)
	run := samples[start:]
	last := run[len(run)-1]
	a.StreakStart = run[0].At
	a.Window, a.WindowReason = s.MetadataTimeout, "metadata acquisition timeout"
	a.Observations, a.Elapsed = qualifying(run, a.GraceEnds, s.PollInterval)
	waiting := last.At.Sub(run[0].At)
	if last.At.Before(a.GraceEnds) {
		a.Status = StatusGrace
		a.Headline = fmt.Sprintf("Fetching metadata; new download grace period ends in %s.", roundDur(a.GraceEnds.Sub(now)))
		q := estimate(last.At, a.GraceEnds, a.Observations, a.Elapsed, s.MetadataTimeout, s)
		a.QualifiesAt = &q
		return
	}
	if a.Observations >= s.MinObservations && a.Elapsed >= s.MetadataTimeout {
		a.Status = StatusQualified
		a.Diagnosis = DiagnosisMetadataTimeout
		a.Headline = fmt.Sprintf("Metadata not acquired after %s of observation (timeout %s).", roundDur(waiting), roundDur(s.MetadataTimeout))
		return
	}
	a.Status = StatusObserving
	q := estimate(last.At, a.GraceEnds, a.Observations, a.Elapsed, s.MetadataTimeout, s)
	a.QualifiesAt = &q
	a.Headline = fmt.Sprintf("Fetching metadata for %s. The metadata timeout would be reached in about %s if unchanged.", roundDur(waiting), roundDur(q.Sub(now)))
}

func roundDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()+0.5))
	case d < 48*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// HumanDuration renders a duration the same way explanations do.
func HumanDuration(d time.Duration) string { return roundDur(d) }
