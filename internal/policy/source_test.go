package policy

import (
	"strings"
	"testing"

	"github.com/voidxela/housekeeparr/internal/domain"
)

func source(v domain.SourceFailure) func(*Sample) {
	return func(s *Sample) { s.SourceFailure = domain.KnownValue(v) }
}

// SPEC §7.2: a tracker's unregistered rejection, on consecutive fresh
// observations without verified progress over the confirmation window,
// is a diagnosis of its own; anything weaker is not.
func TestSourceUnregisteredDiagnosis(t *testing.T) {
	unreg := source(domain.SourceUnregistered)
	// Grace ends at 30 min; the confirmation window (30 min) ends at 60,
	// before the 90-minute no-progress window (120).
	t.Run("qualifies after grace and confirmation", func(t *testing.T) {
		s := newSampler().every(0, 70, 10, domain.StateDownloading, 2*gib, unreg)
		a := eval(t, input(s.out, 0), 70)
		if a.Status != StatusQualified || a.Diagnosis != DiagnosisSourceUnregistered || a.Window != SourceConfirmWindow {
			t.Fatalf("%s/%s: %s", a.Status, a.Diagnosis, a.Headline)
		}
	})
	t.Run("not before the confirmation window", func(t *testing.T) {
		s := newSampler().every(0, 50, 10, domain.StateDownloading, 2*gib, unreg)
		a := eval(t, input(s.out, 0), 50)
		if a.Status == StatusQualified || !strings.Contains(strings.Join(a.Details, " "), "not registered") {
			t.Fatalf("%s/%s: %s %v", a.Status, a.Diagnosis, a.Headline, a.Details)
		}
	})
	t.Run("only consecutive rejections count", func(t *testing.T) {
		s := newSampler().every(0, 40, 10, domain.StateDownloading, 2*gib, unreg).
			add(50, domain.StateDownloading, 2*gib, source(domain.SourceNoFailure)).
			every(60, 80, 10, domain.StateDownloading, 2*gib, unreg)
		if a := eval(t, input(s.out, 0), 80); a.Status == StatusQualified {
			t.Fatalf("a cleared rejection still counted: %s", a.Headline)
		}
	})
	t.Run("verified progress restarts it", func(t *testing.T) {
		s := newSampler().every(0, 40, 10, domain.StateDownloading, 2*gib, unreg).
			every(50, 70, 10, domain.StateDownloading, 3*gib, unreg)
		if a := eval(t, input(s.out, 0), 70); a.Status == StatusQualified {
			t.Fatalf("progressing transfer diagnosed: %s", a.Headline)
		}
	})
	for name, opt := range map[string]func(*Sample){
		"other tracker error": source(domain.SourceTrackerError),
		"no tracker error":    source(domain.SourceNoFailure),
		"unknown":             func(s *Sample) { s.SourceFailure = domain.UnknownValue[domain.SourceFailure]("hidden") },
		"unsupported":         func(s *Sample) { s.SourceFailure = domain.UnsupportedValue[domain.SourceFailure]("n/a") },
		"never recorded":      func(*Sample) {},
	} {
		t.Run(name+" is not a source failure", func(t *testing.T) {
			s := newSampler().every(0, 70, 10, domain.StateDownloading, 2*gib, opt)
			if a := eval(t, input(s.out, 0), 70); a.Status == StatusQualified {
				t.Fatalf("%s/%s", a.Status, a.Diagnosis)
			}
		})
	}
	t.Run("paused with a stale rejection is not evaluated", func(t *testing.T) {
		s := newSampler().every(0, 70, 10, domain.StatePaused, 2*gib, unreg)
		if a := eval(t, input(s.out, 0), 70); a.Status != StatusNotApplicable {
			t.Fatalf("%s/%s", a.Status, a.Diagnosis)
		}
	})
	t.Run("no-progress keeps precedence", func(t *testing.T) {
		s := newSampler().every(0, 120, 10, domain.StateDownloading, 2*gib, unreg)
		if a := eval(t, input(s.out, 0), 120); a.Diagnosis != DiagnosisNoProgress {
			t.Fatalf("%s/%s", a.Status, a.Diagnosis)
		}
	})
}
