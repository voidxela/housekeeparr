package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidxela/housekeeparr/internal/domain"
)

var ctx = context.Background()

func openTest(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSingleWriterLock(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	_, err := Open(ctx, dir, Options{})
	var le *LockedError
	if !errors.As(err, &le) {
		t.Fatalf("second writer must be refused, got %v", err)
	}
	if le.PID != os.Getpid() {
		t.Fatalf("lock holder pid %d, want %d", le.PID, os.Getpid())
	}
	// Readers are allowed concurrently.
	ro, err := Open(ctx, dir, Options{ReadOnly: true})
	if err != nil {
		t.Fatalf("read-only open while writer holds lock: %v", err)
	}
	if err := ro.Write(ctx, func(*Tx) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only write: %v", err)
	}
	ro.Close()
	s.Close()
	s2, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("reopen after release: %v", err)
	}
	s2.Close()
}

func TestReadOnlyWithoutState(t *testing.T) {
	if _, err := Open(ctx, t.TempDir(), Options{ReadOnly: true}); !errors.Is(err, ErrNoState) {
		t.Fatalf("got %v", err)
	}
}

func TestOriginSeparation(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(ctx, dir, Options{Origin: OriginDemo})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	_, err = Open(ctx, dir, Options{Origin: OriginLive})
	var om *OriginMismatchError
	if !errors.As(err, &om) || om.Have != OriginDemo {
		t.Fatalf("live open of demo state must fail, got %v", err)
	}
	ro, err := Open(ctx, dir, Options{ReadOnly: true})
	if err != nil || ro.Origin() != OriginDemo {
		t.Fatalf("read-only must report demo origin: %v", err)
	}
	ro.Close()
}

func TestRefusesNewerSchema(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	if _, err := s.w.Exec("UPDATE meta SET value='99' WHERE key='schema_version'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, dir, Options{}); !errors.Is(err, ErrNewerSchema) {
		t.Fatalf("got %v", err)
	}
}

func transfer(id string, class domain.StateClass, verified int64) domain.Transfer {
	return domain.Transfer{
		ClientID: "qbit", NativeID: id, Hashes: domain.Hashes{V1: id}, Name: "name-" + id, NativeState: string(class), StateClass: class,
		WantedBytes: domain.KnownValue[int64](100), VerifiedBytes: domain.KnownValue(verified),
	}
}

func TestSnapshotPresenceAndDisappearance(t *testing.T) {
	s := openTest(t, t.TempDir())
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	save := func(complete bool, at time.Time, ts ...domain.Transfer) []GoneTransfer {
		var gone []GoneTransfer
		err := s.Write(ctx, func(tx *Tx) error {
			var err error
			gone, err = tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: at, Transfers: ts, Complete: complete}, "r", 0, 1000)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return gone
	}
	save(true, now, transfer("a", domain.StateDownloading, 1), transfer("b", domain.StateComplete, 100))
	// Incomplete listing never establishes disappearance.
	if gone := save(false, now.Add(time.Minute), transfer("a", domain.StateDownloading, 2)); len(gone) != 0 {
		t.Fatalf("partial listing marked gone: %+v", gone)
	}
	gone := save(true, now.Add(2*time.Minute), transfer("a", domain.StateDownloading, 3))
	if len(gone) != 1 || gone[0].NativeID != "b" {
		t.Fatalf("gone = %+v", gone)
	}
	obs, err := s.Observations(ctx, "qbit", "a", now.Add(-time.Hour), 100)
	if err != nil || len(obs) != 3 {
		t.Fatalf("observations for incomplete transfer: %d %v", len(obs), err)
	}
	// Complete-and-unchanged transfers are not sampled.
	obs, _ = s.Observations(ctx, "qbit", "b", now.Add(-time.Hour), 100)
	if len(obs) != 0 {
		t.Fatalf("complete transfer sampled %d times", len(obs))
	}
	refs := []TransferRef{}
	_ = s.Write(ctx, func(tx *Tx) error {
		var err error
		refs, err = tx.TransfersByHash("A")
		return err
	})
	if len(refs) != 1 || refs[0].NativeID != "a" {
		t.Fatalf("hash lookup: %+v", refs)
	}
}

func TestObservationCapPerTransfer(t *testing.T) {
	s := openTest(t, t.TempDir())
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 15; i++ {
		err := s.Write(ctx, func(tx *Tx) error {
			_, err := tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: now.Add(time.Duration(i) * time.Minute),
				Transfers: []domain.Transfer{transfer("a", domain.StateDownloading, int64(i))}, Complete: true}, "r", 0, 10)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	obs, _ := s.Observations(ctx, "qbit", "a", now.Add(-time.Hour), 100)
	if len(obs) != 10 || obs[len(obs)-1].VerifiedBytes.Value != 14 {
		t.Fatalf("cap not applied correctly: %d obs", len(obs))
	}
}

func TestPrunePreservesUnresolvedFindings(t *testing.T) {
	s := openTest(t, t.TempDir())
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := old.Add(365 * 24 * time.Hour)
	err := s.Write(ctx, func(tx *Tx) error {
		resolvedAt := old
		for _, f := range []*domain.Finding{
			{DedupKey: "k1", Type: "t", Severity: domain.SeverityWarning, State: domain.FindingOpen, Title: "open", FirstSeen: old, LastObserved: old, LastEvaluated: old, EvidenceAt: old, Occurrence: 1,
				Subject: domain.Subject{ClientID: "qbit", NativeID: "gone"}},
			{DedupKey: "k2", Type: "t", Severity: domain.SeverityWarning, State: domain.FindingResolved, Title: "resolved", FirstSeen: old, LastObserved: old, LastEvaluated: old, EvidenceAt: old, ResolvedAt: &resolvedAt, Occurrence: 1},
		} {
			if err := tx.InsertFinding(f); err != nil {
				return err
			}
		}
		if _, err := tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: old, Complete: true,
			Transfers: []domain.Transfer{transfer("gone", domain.StateDownloading, 1), transfer("other", domain.StateDownloading, 1)}}, "r", 0, 10); err != nil {
			return err
		}
		if _, err := tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: old.Add(time.Minute), Complete: true}, "r", 0, 10); err != nil {
			return err
		}
		_, err := tx.Prune(now, Retention{Observations: 24 * time.Hour, ConnectionChecks: 24 * time.Hour, ResolvedFindings: 24 * time.Hour, AuditEvents: 24 * time.Hour})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	fs, _ := s.Findings(ctx, FindingFilter{})
	if len(fs) != 1 || fs[0].DedupKey != "k1" {
		t.Fatalf("findings after prune: %+v", fs)
	}
	if tr, _ := s.Transfer(ctx, "qbit", "gone"); tr == nil {
		t.Fatal("transfer referenced by unresolved finding was pruned")
	}
	if tr, _ := s.Transfer(ctx, "qbit", "other"); tr != nil {
		t.Fatal("unreferenced gone transfer was not pruned")
	}
}

func TestPruneKeepsGonePackTransferUntilItsFollowUpEnds(t *testing.T) {
	s := openTest(t, t.TempDir())
	in := testIntent("pack")
	in.Workflow = "partial_pack"
	in.Subject = []byte(`{"manager_id":"sonarr","client_id":"qbit","native_id":"gone"}`)
	id := create(t, s, in)
	if err := s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: t0, Complete: true,
			Transfers: []domain.Transfer{transfer("gone", domain.StateComplete, 1)}}, "r", 0, 10); err != nil {
			return err
		}
		if _, err := tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: t0.Add(time.Minute), Complete: true}, "r", 0, 10); err != nil {
			return err
		}
		return tx.MarkPackFollowUp("sonarr", "episode:1", "pack follow-up", id, t0)
	}); err != nil {
		t.Fatal(err)
	}
	prune := func() {
		t.Helper()
		if err := s.Write(ctx, func(tx *Tx) error {
			_, err := tx.Prune(t0.Add(2*time.Hour), Retention{Observations: time.Hour, AuditEvents: time.Hour})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	prune()
	if tr, err := s.Transfer(ctx, "qbit", "gone"); err != nil || tr == nil || tr.Present {
		t.Fatalf("pack follow-up lost the gone transfer's policy data: %+v, %v", tr, err)
	}
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.SetMissingOrigin("sonarr", "episode:1", "missing")
	}); err != nil {
		t.Fatal(err)
	}
	prune()
	if tr, err := s.Transfer(ctx, "qbit", "gone"); err != nil || tr != nil {
		t.Fatalf("expired transfer kept after its pack follow-up: %+v, %v", tr, err)
	}
}

func TestUniqueActiveFindingPerKey(t *testing.T) {
	s := openTest(t, t.TempDir())
	now := time.Now()
	err := s.Write(ctx, func(tx *Tx) error {
		for i := 0; i < 2; i++ {
			f := &domain.Finding{DedupKey: "same", Type: "t", Severity: domain.SeverityNotice, State: domain.FindingOpen, Title: "x",
				FirstSeen: now, LastObserved: now, LastEvaluated: now, EvidenceAt: now, Occurrence: 1}
			if err := tx.InsertFinding(f); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("two unresolved findings with one dedup key must be rejected by the schema")
	}
}

func TestBackupOnMigrationPathAndRetention(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	for i := 0; i < 7; i++ {
		if _, err := s.Backup(ctx, "manual"); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "backups"))
	if len(entries) > 5 {
		t.Fatalf("%d backups kept, want at most 5", len(entries))
	}
}

func TestPresentTransferIndexAndUnchangedCompleteSkip(t *testing.T) {
	s := openTest(t, t.TempDir())
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	v2 := strings.Repeat("c", 64)
	hybrid := transfer("hy", domain.StateDownloading, 1)
	hybrid.Hashes = domain.Hashes{V1: strings.Repeat("b", 40), V2: v2}
	done := transfer("done", domain.StateComplete, 100)
	done.ConnectedPeers = domain.KnownValue[int64](3)
	save := func(at time.Time, ts ...domain.Transfer) {
		if err := s.Write(ctx, func(tx *Tx) error {
			_, err := tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: at, Transfers: ts, Complete: true}, "r", 0, 100)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	save(now, hybrid, done)
	var idx TransferIndex
	_ = s.Write(ctx, func(tx *Tx) error { var err error; idx, err = tx.PresentTransferIndex(); return err })
	for _, id := range []string{"hy", strings.Repeat("B", 40), v2, v2[:40]} {
		if refs := idx.Lookup(id); len(refs) != 1 || refs[0].NativeID != "hy" {
			t.Errorf("lookup %s: %+v", id, refs)
		}
	}
	// Only volatile fields change: the complete row is not rewritten.
	done.ConnectedPeers = domain.KnownValue[int64](7)
	save(now.Add(time.Hour), hybrid, done)
	rec, _ := s.Transfer(ctx, "qbit", "done")
	if !rec.LastSeen.Equal(now) {
		t.Fatalf("unchanged complete transfer was rewritten (last_seen %v)", rec.LastSeen)
	}
	if rec.Latest.ConnectedPeers.IsKnown() {
		t.Fatal("volatile signals must not be stored for complete transfers")
	}
	inc, _ := s.Transfer(ctx, "qbit", "hy")
	if !inc.LastSeen.Equal(now.Add(time.Hour)) {
		t.Fatal("incomplete transfers must always refresh last_seen")
	}
}

func TestFingerprintCacheNeverHidesChanges(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	done := transfer("done", domain.StateComplete, 100)
	save := func(st *Store, at time.Time, tr domain.Transfer, fault error) error {
		st.InjectWriteFault(fault)
		defer st.InjectWriteFault(nil)
		return st.Write(ctx, func(tx *Tx) error {
			_, err := tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: at, Transfers: []domain.Transfer{tr}, Complete: true}, "r", 0, 100)
			return err
		})
	}
	_ = save(s, now, done, nil)
	_ = save(s, now.Add(time.Minute), done, nil) // confirmed in cache
	renamed := done
	renamed.Name = "renamed"
	if err := save(s, now.Add(2*time.Minute), renamed, nil); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Transfer(ctx, "qbit", "done"); rec.Name != "renamed" {
		t.Fatal("change to a confirmed complete transfer was not written")
	}
	// A failed write must not leave the cache claiming the new state.
	private := renamed
	private.Private = domain.KnownValue(true)
	if err := save(s, now.Add(3*time.Minute), private, errors.New("disk full")); err == nil {
		t.Fatal("expected injected failure")
	}
	if err := save(s, now.Add(4*time.Minute), private, nil); err != nil {
		t.Fatal(err)
	}
	if rec, _ := s.Transfer(ctx, "qbit", "done"); !rec.Latest.Private.Value {
		t.Fatal("change after a failed write was skipped")
	}
	// Removal clears the cache: re-adding writes the row again.
	if err := s.Write(ctx, func(tx *Tx) error {
		_, err := tx.SaveTransferSnapshot(domain.TransferSnapshot{ClientID: "qbit", CollectedAt: now.Add(5 * time.Minute), Complete: true}, "r", 0, 100)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = save(s, now.Add(6*time.Minute), private, nil)
	if rec, _ := s.Transfer(ctx, "qbit", "done"); !rec.Present {
		t.Fatal("re-added transfer not marked present")
	}
}
