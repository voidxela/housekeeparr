package policy

import (
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

const gib = int64(1) << 30

type sampler struct {
	run   string
	mono  int64
	start time.Time
	out   []Sample
}

func newSampler() *sampler { return &sampler{run: "run-1", start: t0} }

// add appends a sample at offset minutes from t0.
func (s *sampler) add(minutes float64, state domain.StateClass, verified int64, opts ...func(*Sample)) *sampler {
	at := t0.Add(time.Duration(minutes * float64(time.Minute)))
	smp := Sample{
		At:             at,
		RunID:          s.run,
		MonoMS:         at.Sub(s.start).Milliseconds() + s.mono,
		StateClass:     state,
		NativeState:    string(state),
		WantedBytes:    domain.KnownValue(10 * gib),
		VerifiedBytes:  domain.KnownValue(verified),
		Availability:   domain.UnknownValue[float64]("not reported"),
		AllFilesWanted: domain.KnownValue(true),
	}
	for _, o := range opts {
		o(&smp)
	}
	s.out = append(s.out, smp)
	return s
}

// every adds samples from..to (inclusive) every step minutes.
func (s *sampler) every(from, to, step float64, state domain.StateClass, verified int64, opts ...func(*Sample)) *sampler {
	for m := from; m <= to+1e-9; m += step {
		s.add(m, state, verified, opts...)
	}
	return s
}

func avail(v float64) func(*Sample) {
	return func(s *Sample) { s.Availability = domain.KnownValue(v) }
}

func wanted(v int64) func(*Sample) {
	return func(s *Sample) { s.WantedBytes = domain.KnownValue(v) }
}

func input(samples []Sample, addedMinutes float64) TransferInput {
	return TransferInput{
		AddedAt:   domain.KnownValue(t0.Add(time.Duration(addedMinutes * float64(time.Minute)))),
		FirstSeen: samples[0].At,
		Samples:   samples,
	}
}

func at(minutes float64) time.Time { return t0.Add(time.Duration(minutes * float64(time.Minute))) }

func eval(t *testing.T, in TransferInput, nowMinutes float64) Assessment {
	t.Helper()
	return EvaluateTransfer(in, DefaultSettings(), at(nowMinutes))
}

func TestSustainedNoProgressQualifiesAfterGraceAndWindow(t *testing.T) {
	// Added at t0; grace ends at 30 min; window 90 min → qualifies at 120.
	s := newSampler().every(0, 110, 10, domain.StateDownloading, 2*gib)
	a := eval(t, input(s.out, 0), 110)
	if a.Status != StatusObserving {
		t.Fatalf("at 110 min: status %s (%s)", a.Status, a.Headline)
	}
	if a.QualifiesAt == nil || !a.QualifiesAt.Equal(at(120)) {
		t.Fatalf("qualifies at %v, want %v", a.QualifiesAt, at(120))
	}
	s.add(120, domain.StateDownloading, 2*gib)
	a = eval(t, input(s.out, 0), 120)
	if a.Status != StatusQualified || a.Diagnosis != DiagnosisNoProgress {
		t.Fatalf("at 120 min: %s/%s (%s)", a.Status, a.Diagnosis, a.Headline)
	}
	if a.Availability != AvailUnknown {
		t.Fatalf("availability must stay unknown, got %s", a.Availability)
	}
	if a.Elapsed != 90*time.Minute || a.Observations != 10 {
		t.Fatalf("elapsed %s, observations %d", a.Elapsed, a.Observations)
	}
}

func TestGracePeriod(t *testing.T) {
	s := newSampler().every(0, 20, 10, domain.StateDownloading, 0)
	a := eval(t, input(s.out, 0), 20)
	if a.Status != StatusGrace {
		t.Fatalf("status %s", a.Status)
	}
	if a.QualifiesAt == nil || !a.QualifiesAt.Equal(at(120)) {
		t.Fatalf("qualifies at %v", a.QualifiesAt)
	}
	if a.GraceFrom != "client added time" {
		t.Fatalf("grace source %q", a.GraceFrom)
	}
}

func TestGraceFromFirstSeenWhenAddedTimeUnknown(t *testing.T) {
	s := newSampler().every(0, 20, 10, domain.StateDownloading, 0)
	in := TransferInput{AddedAt: domain.UnknownValue[time.Time]("n/a"), FirstSeen: at(0), Samples: s.out}
	a := eval(t, in, 20)
	if a.Status != StatusGrace || !strings.HasPrefix(a.GraceFrom, "first observation") || len(a.Limitations) == 0 {
		t.Fatalf("%s %q %v", a.Status, a.GraceFrom, a.Limitations)
	}
}

func TestSlowSustainedProgressNeverQualifies(t *testing.T) {
	s := newSampler()
	verified := int64(0)
	for m := 0.0; m <= 600; m += 10 {
		if int(m)%30 == 0 { // one 4 MiB piece every 30 minutes
			verified += 4 << 20
		}
		s.add(m, domain.StateDownloading, verified)
	}
	for _, now := range []float64{120, 300, 600} {
		a := eval(t, input(s.out[:int(now/10)+1], 0), now)
		if a.Status == StatusQualified {
			t.Fatalf("slow progress qualified at %v min: %s", now, a.Headline)
		}
	}
}

func TestDiscardedTrafficDoesNotPostponeDiagnosis(t *testing.T) {
	// Network traffic is not part of the sample; verified data is flat even
	// though the client may be receiving (and discarding) corrupt pieces.
	s := newSampler().every(0, 120, 10, domain.StateDownloading, 5*gib)
	if a := eval(t, input(s.out, 0), 120); a.Status != StatusQualified {
		t.Fatalf("status %s", a.Status)
	}
}

func TestPausedQueuedCheckingAreNotStalls(t *testing.T) {
	for _, st := range []domain.StateClass{domain.StatePaused, domain.StateQueued, domain.StateChecking, domain.StateComplete, domain.StateError} {
		s := newSampler().every(0, 300, 10, st, 2*gib)
		a := eval(t, input(s.out, 0), 300)
		if a.Status != StatusNotApplicable || a.Diagnosis != DiagnosisNone {
			t.Errorf("%s: %s/%s", st, a.Status, a.Diagnosis)
		}
	}
}

func TestPauseResetsQualification(t *testing.T) {
	s := newSampler().
		every(0, 100, 10, domain.StateDownloading, 1*gib).
		add(110, domain.StatePaused, 1*gib).
		every(120, 200, 10, domain.StateDownloading, 1*gib)
	a := eval(t, input(s.out, 0), 200)
	if a.Status != StatusObserving {
		t.Fatalf("pause must reset streak; status %s", a.Status)
	}
	if !a.StreakStart.Equal(at(120)) {
		t.Fatalf("streak start %v", a.StreakStart)
	}
	if len(a.Resets) == 0 || !strings.Contains(a.Resets[0].Reason, "paused") {
		t.Fatalf("resets %+v", a.Resets)
	}
}

func TestRecheckDecreaseAndFileSelectionResetBaseline(t *testing.T) {
	s := newSampler().
		every(0, 100, 10, domain.StateDownloading, 3*gib).
		every(110, 190, 10, domain.StateDownloading, 2*gib) // verified dropped
	a := eval(t, input(s.out, 0), 190)
	if a.Status != StatusObserving || !strings.Contains(a.Resets[len(a.Resets)-1].Reason, "decreased") {
		t.Fatalf("decrease: %s %+v", a.Status, a.Resets)
	}
	s = newSampler().
		every(0, 100, 10, domain.StateDownloading, 2*gib).
		every(110, 190, 10, domain.StateDownloading, 2*gib, wanted(8*gib))
	a = eval(t, input(s.out, 0), 190)
	if a.Status != StatusObserving || !strings.Contains(a.Resets[len(a.Resets)-1].Reason, "file selection") {
		t.Fatalf("selection: %s %+v", a.Status, a.Resets)
	}
}

func TestObservationGapNeverCountsAsEvidence(t *testing.T) {
	// A 30-minute gap (> 20 min max) restarts qualification.
	s := newSampler().
		every(0, 80, 10, domain.StateDownloading, gib).
		every(110, 190, 10, domain.StateDownloading, gib)
	a := eval(t, input(s.out, 0), 190)
	if a.Status != StatusObserving || !a.StreakStart.Equal(at(110)) {
		t.Fatalf("gap: %s start %v", a.Status, a.StreakStart)
	}
	// A tolerated 20-minute gap is credited only one poll interval.
	s = newSampler().
		every(30, 90, 10, domain.StateDownloading, gib).
		every(110, 130, 10, domain.StateDownloading, gib)
	a = eval(t, input(s.out, 0), 130)
	if a.Elapsed != 90*time.Minute { // 100 wall minutes, 10 not credited
		t.Fatalf("elapsed %s, want 90m", a.Elapsed)
	}
}

func TestClockAnomaliesRequireRenewedEvidence(t *testing.T) {
	// Backward step.
	s := newSampler().every(0, 100, 10, domain.StateDownloading, gib)
	s.add(95, domain.StateDownloading, gib)
	s.every(105, 125, 10, domain.StateDownloading, gib)
	a := eval(t, input(s.out, 0), 125)
	if a.Status == StatusQualified || !strings.Contains(a.Resets[0].Reason, "backward") {
		t.Fatalf("backward: %s %+v", a.Status, a.Resets)
	}
	// Forward jump within one run: wall +15 min while monotonic +5 min.
	s = newSampler().every(0, 100, 10, domain.StateDownloading, gib)
	s.mono = -10 * 60 * 1000 // subsequent monotonic readings lag wall time by 10 minutes
	s.add(115, domain.StateDownloading, gib)
	s.every(125, 135, 10, domain.StateDownloading, gib)
	a = eval(t, input(s.out, 0), 135)
	if a.Status == StatusQualified || !strings.Contains(a.Resets[0].Reason, "jumped") {
		t.Fatalf("jump: %s %+v", a.Status, a.Resets)
	}
	// Process restart: different run IDs do not compare monotonic clocks.
	s = newSampler().every(0, 60, 10, domain.StateDownloading, gib)
	s.run, s.mono = "run-2", -999999
	s.every(70, 120, 10, domain.StateDownloading, gib)
	if a := eval(t, input(s.out, 0), 120); a.Status != StatusQualified {
		t.Fatalf("restart without clock anomaly should still qualify: %s %+v", a.Status, a.Resets)
	}
	// Current time earlier than the evidence.
	s = newSampler().every(0, 120, 10, domain.StateDownloading, gib)
	if a := eval(t, input(s.out, 0), 100); a.Status != StatusStale {
		t.Fatalf("clock behind evidence: %s", a.Status)
	}
}

func TestStaleEvidenceIsNotEvaluated(t *testing.T) {
	s := newSampler().every(0, 120, 10, domain.StateDownloading, gib)
	if a := eval(t, input(s.out, 0), 150); a.Status != StatusStale || a.Diagnosis != DiagnosisNone {
		t.Fatalf("status %s", a.Status)
	}
}

func TestNearCompleteUsesLongerWindow(t *testing.T) {
	v := 10 * gib * 96 / 100
	s := newSampler().every(0, 200, 10, domain.StateDownloading, v)
	a := eval(t, input(s.out, 0), 200)
	if a.Status != StatusObserving || a.Window != 4*time.Hour {
		t.Fatalf("%s window %s", a.Status, a.Window)
	}
	s.every(210, 270, 10, domain.StateDownloading, v)
	if a := eval(t, input(s.out, 0), 270); a.Status != StatusQualified {
		t.Fatalf("status %s at 270 (%s)", a.Status, a.Headline)
	}
}

func TestMinimumObservationsRequired(t *testing.T) {
	st := DefaultSettings()
	st.NoProgressWindow = 15 * time.Minute
	st.NewDownloadGrace = 0
	s := newSampler().add(0, domain.StateDownloading, gib).add(10, domain.StateDownloading, gib).add(20, domain.StateDownloading, gib)
	a := EvaluateTransfer(input(s.out[:2], -60), st, at(10))
	if a.Status == StatusQualified {
		t.Fatal("two observations must not qualify")
	}
	a = EvaluateTransfer(input(s.out, -60), st, at(20))
	if a.Status != StatusQualified {
		t.Fatalf("three observations, 20 min: %s", a.Status)
	}
}

func TestPieceAvailabilityEvidence(t *testing.T) {
	s := newSampler().every(0, 120, 10, domain.StateDownloading, gib, avail(0.6))
	a := eval(t, input(s.out, 0), 120)
	if a.Diagnosis != DiagnosisNoProgressUnavailable || a.Availability != AvailLacking {
		t.Fatalf("lacking: %s %s", a.Diagnosis, a.Availability)
	}
	// One unknown sample in the window keeps availability unknown.
	s = newSampler().every(0, 110, 10, domain.StateDownloading, gib, avail(0.6))
	s.add(120, domain.StateDownloading, gib)
	a = eval(t, input(s.out, 0), 120)
	if a.Diagnosis != DiagnosisNoProgress || a.Availability != AvailUnknown {
		t.Fatalf("unknown: %s %s", a.Diagnosis, a.Availability)
	}
	// Deselected files make availability non-specific.
	notAll := func(s *Sample) { s.AllFilesWanted = domain.KnownValue(false) }
	s = newSampler().every(0, 120, 10, domain.StateDownloading, gib, avail(0.2), notAll)
	a = eval(t, input(s.out, 0), 120)
	if a.Diagnosis != DiagnosisNoProgress || a.Availability != AvailNotSpecific {
		t.Fatalf("not specific: %s %s", a.Diagnosis, a.Availability)
	}
	// Negative values mean "not computed" and are unknown.
	s = newSampler().every(0, 120, 10, domain.StateDownloading, gib, avail(-1))
	if a := eval(t, input(s.out, 0), 120); a.Availability != AvailUnknown {
		t.Fatalf("negative availability: %s", a.Availability)
	}
}

func TestMetadataTimeout(t *testing.T) {
	meta := func(s *Sample) {
		s.WantedBytes = domain.UnknownValue[int64]("no metadata")
		s.VerifiedBytes = domain.UnknownValue[int64]("no metadata")
	}
	s := newSampler().every(0, 80, 10, domain.StateMetadata, 0, meta)
	a := eval(t, input(s.out, 0), 80)
	if a.Status != StatusObserving {
		t.Fatalf("at 80: %s", a.Status)
	}
	s.every(90, 90, 10, domain.StateMetadata, 0, meta)
	a = eval(t, input(s.out, 0), 90)
	if a.Status != StatusQualified || a.Diagnosis != DiagnosisMetadataTimeout {
		t.Fatalf("at 90: %s %s", a.Status, a.Diagnosis)
	}
}

func TestUnknownVerifiedProgressIsNotEvaluated(t *testing.T) {
	unk := func(s *Sample) { s.VerifiedBytes = domain.UnsupportedValue[int64]("not exposed") }
	s := newSampler().every(0, 300, 10, domain.StateDownloading, 0, unk)
	a := eval(t, input(s.out, 0), 300)
	if a.Status != StatusNoEvidence || a.Diagnosis != DiagnosisNone {
		t.Fatalf("status %s", a.Status)
	}
}

func TestProgressingStatus(t *testing.T) {
	s := newSampler().every(0, 100, 10, domain.StateDownloading, gib).add(110, domain.StateDownloading, 2*gib)
	if a := eval(t, input(s.out, 0), 110); a.Status != StatusProgressing || a.LastProgressAt == nil {
		t.Fatalf("status %s", a.Status)
	}
}

func TestRevisionIsStableAndSensitive(t *testing.T) {
	a, b := DefaultSettings(), DefaultSettings()
	if a.Revision() != b.Revision() {
		t.Fatal("revision not deterministic")
	}
	b.NoProgressWindow += time.Minute
	if a.Revision() == b.Revision() {
		t.Fatal("revision insensitive to changes")
	}
	c := DefaultSettings()
	c.RequirePiecesUnavailable = true
	if a.Revision() == c.Revision() {
		t.Fatal("revision insensitive to RequirePiecesUnavailable")
	}
}

func TestRequirePiecesUnavailable(t *testing.T) {
	notAll := func(s *Sample) { s.AllFilesWanted = domain.KnownValue(false) }

	cases := []struct {
		name      string
		opts      []func(*Sample)
		wantAvail AvailabilityEvidence
		wantDesc  string
	}{
		{
			name:      "unknown availability",
			opts:      nil,
			wantAvail: AvailUnknown,
			wantDesc:  "unknown",
		},
		{
			name:      "not specific to wanted data",
			opts:      []func(*Sample){avail(0.2), notAll},
			wantAvail: AvailNotSpecific,
			wantDesc:  "not specific to wanted data",
		},
		{
			name:      "available among connected peers",
			opts:      []func(*Sample){avail(1.5)},
			wantAvail: AvailSeenComplete,
			wantDesc:  "available among connected peers",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSampler().every(0, 120, 10, domain.StateDownloading, gib, tc.opts...)
			in := input(s.out, 0)

			// Default (RequirePiecesUnavailable == false): qualifies.
			off := EvaluateTransfer(in, DefaultSettings(), at(120))
			if off.Status != StatusQualified || off.Diagnosis != DiagnosisNoProgress {
				t.Fatalf("with setting off: got %s/%s (%s), want qualified/no_progress", off.Status, off.Diagnosis, off.Headline)
			}
			if off.Availability != tc.wantAvail {
				t.Fatalf("with setting off: availability = %s, want %s", off.Availability, tc.wantAvail)
			}

			// RequirePiecesUnavailable == true: must not qualify.
			st := DefaultSettings()
			st.RequirePiecesUnavailable = true
			on := EvaluateTransfer(in, st, at(120))
			if on.Status != StatusObserving || on.Diagnosis != DiagnosisNone {
				t.Fatalf("with setting on: got %s/%s (%s), want observing/none", on.Status, on.Diagnosis, on.Headline)
			}
			if on.Availability != tc.wantAvail {
				t.Fatalf("with setting on: availability = %s, want %s", on.Availability, tc.wantAvail)
			}
			if on.QualifiesAt != nil {
				t.Fatalf("with setting on: QualifiesAt = %v, want nil (must not promise qualification by time alone)", on.QualifiesAt)
			}
			for _, sub := range []string{"elapsed", "connected peers lack the wanted pieces", tc.wantDesc} {
				if !strings.Contains(on.Headline, sub) {
					t.Errorf("headline %q missing %q", on.Headline, sub)
				}
			}
		})
	}

	t.Run("lacking availability still qualifies as no_progress_pieces_unavailable", func(t *testing.T) {
		s := newSampler().every(0, 120, 10, domain.StateDownloading, gib, avail(0.6))
		st := DefaultSettings()
		st.RequirePiecesUnavailable = true
		a := EvaluateTransfer(input(s.out, 0), st, at(120))
		if a.Status != StatusQualified || a.Diagnosis != DiagnosisNoProgressUnavailable || a.Availability != AvailLacking {
			t.Fatalf("got %s/%s (%s), avail=%s", a.Status, a.Diagnosis, a.Headline, a.Availability)
		}
	})

	t.Run("metadata timeout unaffected", func(t *testing.T) {
		meta := func(s *Sample) {
			s.WantedBytes = domain.UnknownValue[int64]("no metadata")
			s.VerifiedBytes = domain.UnknownValue[int64]("no metadata")
		}
		s := newSampler().every(0, 90, 10, domain.StateMetadata, 0, meta)
		st := DefaultSettings()
		st.RequirePiecesUnavailable = true
		a := EvaluateTransfer(input(s.out, 0), st, at(90))
		if a.Status != StatusQualified || a.Diagnosis != DiagnosisMetadataTimeout {
			t.Fatalf("got %s/%s (%s), want qualified/metadata_timeout", a.Status, a.Diagnosis, a.Headline)
		}
	})
}

func TestWantedOnlyAvailabilityIgnoresFileSelection(t *testing.T) {
	wantedOnly := func(s *Sample) {
		s.AllFilesWanted = domain.KnownValue(false)
		s.AvailabilityWantedOnly = true
	}
	s := newSampler().every(0, 120, 10, domain.StateDownloading, gib, avail(0.4), wantedOnly)
	a := eval(t, input(s.out, 0), 120)
	if a.Availability != AvailLacking || a.Diagnosis != DiagnosisNoProgressUnavailable {
		t.Fatalf("%s %s", a.Availability, a.Diagnosis)
	}
}

// Neither a complete payload in an error-like state nor a tracker that
// refuses the client is a dead release: the same no-progress streak that
// qualifies otherwise is not diagnosed.
func TestCompleteOrClientRefusedIsNotADeadRelease(t *testing.T) {
	complete := newSampler().every(0, 120, 10, domain.StateDownloading, 10*gib)
	if a := eval(t, input(complete.out, 0), 120); a.Status != StatusNotApplicable || !strings.Contains(a.Headline, "payload is complete") {
		t.Fatalf("complete payload: %s/%s (%s)", a.Status, a.Diagnosis, a.Headline)
	}
	refused := func(s *Sample) { s.SourceFailure = domain.KnownValue(domain.SourceClientRejected) }
	r := newSampler().every(0, 120, 10, domain.StateDownloading, 2*gib, refused)
	if a := eval(t, input(r.out, 0), 120); a.Status != StatusNotApplicable || !strings.Contains(a.Headline, "refuses this client") {
		t.Fatalf("client refused: %s/%s (%s)", a.Status, a.Diagnosis, a.Headline)
	}
	// Once the refusal stops (an upgraded client), the stall is evaluated
	// again from the evidence.
	r.add(130, domain.StateDownloading, 2*gib)
	if a := eval(t, input(r.out, 0), 130); a.Status != StatusQualified {
		t.Fatalf("after the refusal ended: %s (%s)", a.Status, a.Headline)
	}
}
