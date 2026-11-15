package findings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/store"
)

var ctx = context.Background()
var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(ctx, t.TempDir(), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func draft(key, title string, sev domain.Severity) Draft {
	return Draft{DedupKey: key, Type: domain.FindingTransferStalled, Severity: sev, Title: title, Summary: "s", EvidenceAt: t0,
		Subject: domain.Subject{ClientID: "qbit", NativeID: key}}
}

func apply(t *testing.T, s *store.Store, ev Evaluation, now time.Time) Result {
	t.Helper()
	var r Result
	if err := s.Write(ctx, func(tx *store.Tx) error {
		var err error
		r, err = Apply(tx, ev, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDeduplicationAndUpdate(t *testing.T) {
	s := openStore(t)
	ev := Evaluation{Scope: "transfer:qbit:", Drafts: []Draft{draft("transfer:qbit:a", "No progress", domain.SeverityWarning)}}
	r := apply(t, s, ev, t0)
	if len(r.Opened) != 1 {
		t.Fatalf("opened %d", len(r.Opened))
	}
	ev.Drafts[0].Title = "No progress (pieces unavailable)"
	r = apply(t, s, ev, t0.Add(10*time.Minute))
	if len(r.Opened) != 0 || len(r.Updated) != 1 {
		t.Fatalf("repeat observation must update the same finding: %+v", r)
	}
	all, _ := s.Findings(ctx, store.FindingFilter{})
	if len(all) != 1 || !all[0].FirstSeen.Equal(t0) || !all[0].LastObserved.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("findings %+v", all)
	}
}

func TestResolutionOnlyWithinAuthoritativeScope(t *testing.T) {
	s := openStore(t)
	apply(t, s, Evaluation{Scope: "transfer:qbit:", Drafts: []Draft{draft("transfer:qbit:a", "x", domain.SeverityWarning)}}, t0)
	apply(t, s, Evaluation{Scope: "transfer:other:", Drafts: nil}, t0)
	if fs, _ := s.Findings(ctx, store.FindingFilter{States: []domain.FindingState{domain.FindingOpen}}); len(fs) != 1 {
		t.Fatal("evaluation of another scope must not resolve this finding")
	}
	// Kept keys (not re-evaluable) are preserved.
	apply(t, s, Evaluation{Scope: "transfer:qbit:", Keep: map[string]bool{"transfer:qbit:a": true}}, t0)
	if fs, _ := s.Findings(ctx, store.FindingFilter{States: []domain.FindingState{domain.FindingOpen}}); len(fs) != 1 {
		t.Fatal("kept finding was resolved")
	}
	r := apply(t, s, Evaluation{Scope: "transfer:qbit:", Resolve: func(domain.Finding) Resolution {
		return Resolution{Reason: "Transfer no longer present.", Limitation: "Cause of removal unknown."}
	}}, t0.Add(time.Hour))
	if len(r.Resolved) != 1 || r.Resolved[0].Resolution != "Transfer no longer present." {
		t.Fatalf("resolved %+v", r.Resolved)
	}
	f, _ := s.Finding(ctx, r.Resolved[0].ID)
	if f.State != domain.FindingResolved || len(f.Detail.Limitations) != 1 {
		t.Fatalf("finding %+v", f)
	}
}

func TestRecurrenceLinksPreviousOccurrence(t *testing.T) {
	s := openStore(t)
	ev := Evaluation{Scope: "transfer:qbit:", Drafts: []Draft{draft("transfer:qbit:a", "x", domain.SeverityWarning)}}
	first := apply(t, s, ev, t0).Opened[0]
	apply(t, s, Evaluation{Scope: "transfer:qbit:"}, t0.Add(time.Hour))
	r := apply(t, s, ev, t0.Add(2*time.Hour))
	if len(r.Recurred) != 1 {
		t.Fatalf("expected recurrence: %+v", r)
	}
	again := r.Recurred[0]
	if again.ID == first.ID || again.PreviousID == nil || *again.PreviousID != first.ID || again.Occurrence != 2 {
		t.Fatalf("recurrence %+v", again)
	}
	hist, _ := s.FindingHistory(ctx, again, 10)
	if len(hist) != 1 || hist[0].ID != first.ID {
		t.Fatalf("history %+v", hist)
	}
}

func TestAcknowledgeKeepsFindingVisibleAndUpdated(t *testing.T) {
	s := openStore(t)
	ev := Evaluation{Scope: "k:", Drafts: []Draft{draft("k:1", "x", domain.SeverityWarning)}}
	f := apply(t, s, ev, t0).Opened[0]
	if err := s.Write(ctx, func(tx *store.Tx) error { _, err := Acknowledge(tx, f.ID, "cli", t0); return err }); err != nil {
		t.Fatal(err)
	}
	ev.Drafts[0].Severity = domain.SeverityError
	apply(t, s, ev, t0.Add(time.Minute))
	got, _ := s.Finding(ctx, f.ID)
	if got.State != domain.FindingAcknowledged || got.Severity != domain.SeverityError {
		t.Fatalf("acknowledged finding must keep state and receive updates: %+v", got)
	}
	evs, _ := s.FindingEvents(ctx, f.ID)
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) != 3 || kinds[1] != "acknowledged" || kinds[2] != "updated" {
		t.Fatalf("events %v", kinds)
	}
}

func TestSnoozeBoundsAndExpiry(t *testing.T) {
	s := openStore(t)
	f := apply(t, s, Evaluation{Scope: "k:", Drafts: []Draft{draft("k:1", "x", domain.SeverityWarning)}}, t0).Opened[0]
	err := s.Write(ctx, func(tx *store.Tx) error { _, err := Snooze(tx, f.ID, time.Minute, "web", t0); return err })
	if !errors.Is(err, ErrInvalidSnooze) {
		t.Fatalf("got %v", err)
	}
	if err := s.Write(ctx, func(tx *store.Tx) error { _, err := Snooze(tx, f.ID, 2*time.Hour, "web", t0); return err }); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = s.Write(ctx, func(tx *store.Tx) error { n, err = ExpireSnoozes(tx, t0.Add(time.Hour)); return err })
	if n != 0 {
		t.Fatal("snooze expired early")
	}
	_ = s.Write(ctx, func(tx *store.Tx) error { n, err = ExpireSnoozes(tx, t0.Add(3*time.Hour)); return err })
	got, _ := s.Finding(ctx, f.ID)
	if n != 1 || got.State != domain.FindingOpen || got.SnoozedUntil != nil {
		t.Fatalf("expiry: n=%d %+v", n, got)
	}
}

func TestActionsOnResolvedFindingsFail(t *testing.T) {
	s := openStore(t)
	f := apply(t, s, Evaluation{Scope: "k:", Drafts: []Draft{draft("k:1", "x", domain.SeverityWarning)}}, t0).Opened[0]
	apply(t, s, Evaluation{Scope: "k:"}, t0)
	err := s.Write(ctx, func(tx *store.Tx) error { _, err := Acknowledge(tx, f.ID, "cli", t0); return err })
	if !errors.Is(err, ErrResolved) {
		t.Fatalf("got %v", err)
	}
	err = s.Write(ctx, func(tx *store.Tx) error { _, err := Acknowledge(tx, 9999, "cli", t0); return err })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestDraftOutsideScopeRejected(t *testing.T) {
	s := openStore(t)
	err := s.Write(ctx, func(tx *store.Tx) error {
		_, err := Apply(tx, Evaluation{Scope: "a:", Drafts: []Draft{draft("b:1", "x", domain.SeverityNotice)}}, t0)
		return err
	})
	if err == nil {
		t.Fatal("expected scope error")
	}
}
