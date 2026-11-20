package store

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/voidxela/housekeeparr/internal/domain"
	"path/filepath"
	"testing"
	"time"
)

// A database at schema 12 opens at schema 13. An outcome whose intent is
// still retained gets its client from the intent's subject; one whose
// intent was pruned stays without a client and never exempts a transfer
// (a hash alone does not identify one). Existing retained rows were all
// recorded by verified stops. The identity survives a restart.
func TestForwardMigrationV12HandledIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "0013" {
			break
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	for _, q := range []string{
		"INSERT INTO meta(key, value) VALUES('schema_version','12'),('data_origin','live'),('instance_id','v12inst'),('created_at','0')",
		`INSERT INTO action_intents(id, idem_key, workflow, origin, subject_json, state, policy_revision, config_revision, instance_id,
		 fingerprint, evidence_json, plan_json, created_at, updated_at)
		 VALUES(1, 'k1', 'no_progress_recovery', 'automatic', '{"client_id":"qbit","native_id":"AA"}', 'succeeded', 'r', 'c', 'v12inst', 'f', '{}', '{}', 0, 0)`,
		`INSERT INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at)
		 VALUES(1, 'sonarr', 'episode:5', 'AA', 1000, 'awaiting_grab', 1000)`,
		// Intent 2 was pruned by retention: its client is unrecoverable.
		`INSERT INTO recovery_outcomes(intent_id, manager_id, content, old_download_id, started_at, state, checked_at)
		 VALUES(2, 'radarr', 'movie:4', 'bb', 1000, 'completed', 1000)`,
		`INSERT INTO retained_transfers(client_id, native_id, intent_id, wanted_bytes, retained_at) VALUES('qbit', 'cc', 3, 10, 1000)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("build v12 database: %v", err)
		}
	}
	db.Close()

	check := func(s *Store) {
		t.Helper()
		rec, err := s.RecoveredDownloads(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rec) != 1 || rec[HandledKey("qbit", "aa")] != 1 {
			t.Fatalf("handled transfers %v (want only qbit|aa from intent 1)", rec)
		}
		ret, err := s.RetainedTransfers(ctx, false, 0)
		if err != nil || len(ret) != 1 || ret[0].Disposition != RetainedStopped {
			t.Fatalf("retained %+v %v", ret, err)
		}
	}
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v12 database: %v", err)
	}
	check(s)
	if err := s.Write(ctx, func(tx *Tx) error {
		return tx.CreateOutcome(RecoveryOutcome{IntentID: 4, ManagerID: "radarr", Content: []string{"movie:9"}, OldDownloadID: "AA",
			OldClientID: "qbit2", StartedAt: time.Unix(2, 0)})
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rec, _ := s.RecoveredDownloads(ctx)
	if len(rec) != 2 || rec[HandledKey("qbit", "aa")] != 1 || rec[HandledKey("qbit2", "aa")] != 4 {
		t.Fatalf("after restart: %v (the same hash on two clients is two transfers)", rec)
	}
}

// A running record is upgraded to stopped by a later verified stop, never
// the reverse, and its sizes follow fresh listings without unknown values
// replacing known ones.
func TestRetainedDispositionAndRefresh(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	zero, ten := int64(0), int64(10)
	write := func(f func(tx *Tx) error) {
		t.Helper()
		if err := s.Write(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	write(func(tx *Tx) error {
		return tx.RecordRetained(RetainedTransfer{ClientID: "q", NativeID: "a", IntentID: 1, Disposition: RetainedRunning, VerifiedBytes: &zero, RetainedAt: time.Unix(1, 0)})
	})
	if u, _ := s.RetainedUsage(ctx); u.Count != 1 || u.Running != 1 || u.UnknownSize != 1 || u.NoPayload != 0 {
		t.Fatalf("running, unknown size: %+v (a running transfer can obtain payload)", u)
	}
	write(func(tx *Tx) error {
		return tx.RecordRetained(RetainedTransfer{ClientID: "q", NativeID: "a", IntentID: 1, Disposition: RetainedStopped, RetainedAt: time.Unix(2, 0)})
	})
	if u, _ := s.RetainedUsage(ctx); u.Count != 1 || u.Running != 0 || u.NoPayload != 1 {
		t.Fatalf("stopped, no payload: %+v", u)
	}
	write(func(tx *Tx) error {
		return tx.RecordRetained(RetainedTransfer{ClientID: "q", NativeID: "a", IntentID: 1, Disposition: RetainedRunning, RetainedAt: time.Unix(3, 0)})
	})
	write(func(tx *Tx) error { return tx.RefreshRetained("q", "a", &ten, nil) })
	write(func(tx *Tx) error { return tx.RefreshRetained("q", "a", nil, nil) })
	if u, _ := s.RetainedUsage(ctx); u.Running != 0 || u.KnownBytes != 10 || u.NoPayload != 0 {
		t.Fatalf("after refresh: %+v", u)
	}
}

// A database at schema 13 opens at schema 14 with an empty notification
// outbox that accepts events and deduplicates them.
func TestForwardMigrationV13Notifications(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := migrationFS.ReadDir("migrations")
	for _, e := range entries {
		if e.Name() >= "0014" {
			break
		}
		body, _ := migrationFS.ReadFile("migrations/" + e.Name())
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	if _, err := db.Exec("INSERT INTO meta(key, value) VALUES('schema_version','13'),('data_origin','live'),('instance_id','v13inst'),('created_at','0')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v13 database: %v", err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		if err := s.Write(ctx, func(tx *Tx) error {
			_, err := tx.EnqueueNotification(Notification{Target: "hook", DedupKey: "opened|1", Kind: "finding.opened", Severity: "warning", Title: "t", Body: "b", CreatedAt: time.Unix(1, 0)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.PendingNotifications(ctx, "hook"); err != nil || n != 1 {
		t.Fatalf("pending %d %v", n, err)
	}
}

// Schema 15: missing content is upserted, marked gone only by a complete
// read, and its search history survives reappearance.
func TestMissingContentLifecycle(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	it := func(id int64) domain.MissingItem {
		return domain.MissingItem{ManagerID: "sonarr", ContentKind: "episode", ContentID: id, SeriesID: 3, Title: "Show", Monitored: true,
			Available: domain.KnownValue(true)}
	}
	save := func(snap domain.MissingSnapshot, at time.Time) int {
		t.Helper()
		var n int
		if err := s.Write(ctx, func(tx *Tx) error { var err error; n, err = tx.SaveMissingSnapshot(snap, at); return err }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	t1 := time.Unix(1000, 0)
	save(domain.MissingSnapshot{ManagerID: "sonarr", Items: []domain.MissingItem{it(1), it(2)}, Complete: true}, t1)
	next := t1.Add(time.Hour)
	if err := s.Write(ctx, func(tx *Tx) error {
		id := int64(7)
		return tx.SetMissingSearch("sonarr", "episode:1", MissingBackoff, "no grab", &id, &t1, &next, true)
	}); err != nil {
		t.Fatal(err)
	}
	// An incomplete read marks nothing gone.
	if n := save(domain.MissingSnapshot{ManagerID: "sonarr", Items: []domain.MissingItem{it(2)}}, t1.Add(time.Minute)); n != 0 {
		t.Fatalf("incomplete read marked %d gone", n)
	}
	if n := save(domain.MissingSnapshot{ManagerID: "sonarr", Items: []domain.MissingItem{it(2)}, Complete: true}, t1.Add(2*time.Minute)); n != 1 {
		t.Fatalf("complete read marked %d gone", n)
	}
	save(domain.MissingSnapshot{ManagerID: "sonarr", Items: []domain.MissingItem{it(1), it(2)}, Complete: true}, t1.Add(3*time.Minute))
	r, _ := s.MissingRecord(ctx, "sonarr", "episode:1")
	if r == nil || !r.Present || r.Searches != 1 || r.NextSearchAt == nil || r.State != MissingNotSearched {
		t.Fatalf("reappeared: %+v", r)
	}
}

// A database at schema 14 (with a queued notification) opens at schema 15.
func TestForwardMigrationV14MissingContent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := migrationFS.ReadDir("migrations")
	for _, e := range entries {
		if e.Name() >= "0015" {
			break
		}
		body, _ := migrationFS.ReadFile("migrations/" + e.Name())
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	for _, q := range []string{
		"INSERT INTO meta(key, value) VALUES('schema_version','14'),('data_origin','live'),('instance_id','v14inst'),('created_at','0')",
		"INSERT INTO notification_events(target, dedup_key, kind, severity, title, body, created_at, next_at) VALUES('hook','k','finding.opened','warning','t','b',1,1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v14 database: %v", err)
	}
	defer s.Close()
	if n, _ := s.PendingNotifications(ctx, "hook"); n != 1 {
		t.Fatalf("queued notification lost: %d", n)
	}
	if list, err := s.MissingContent(ctx, MissingFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("missing content %v %v", list, err)
	}
}

// A database at schema 15 (with missing content) opens at schema 16, and
// a source's streak of good reads carries across reads.
func TestForwardMigrationV15Sources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := migrationFS.ReadDir("migrations")
	for _, e := range entries {
		if e.Name() >= "0016" {
			break
		}
		body, _ := migrationFS.ReadFile("migrations/" + e.Name())
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	for _, q := range []string{
		"INSERT INTO meta(key, value) VALUES('schema_version','15'),('data_origin','live'),('instance_id','v15inst'),('created_at','0')",
		"INSERT INTO missing_content(manager_id, content_key, title, first_seen, last_seen) VALUES('sonarr','episode:1','t',1,1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v15 database: %v", err)
	}
	defer s.Close()
	if r, _ := s.MissingRecord(ctx, "sonarr", "episode:1"); r == nil {
		t.Fatal("missing content lost")
	}
	at := time.Unix(1000, 0)
	for i := 0; i < 2; i++ {
		if err := s.Write(ctx, func(tx *Tx) error {
			return tx.SaveSources(domain.SourceSnapshot{ManagerID: "sonarr", CollectedAt: at, Sources: []domain.Source{{ID: 1, Name: "T", Protocol: "torrent", AutomaticSearch: true, StatusKnown: true}}})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := s.Sources(ctx, "sonarr"); len(list) != 1 || list[0].OKStreak != 2 {
		t.Fatalf("sources %+v", list)
	}
}

// A database at schema 16 opens at 17; existing source rows were read with
// status, so they keep counting as known.
func TestForwardMigrationV16SourceStatusKnown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := migrationFS.ReadDir("migrations")
	for _, e := range entries {
		if e.Name() >= "0017" {
			break
		}
		body, _ := migrationFS.ReadFile("migrations/" + e.Name())
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	for _, q := range []string{
		"INSERT INTO meta(key, value) VALUES('schema_version','16'),('data_origin','live'),('instance_id','v16inst'),('created_at','0')",
		"INSERT INTO source_status(manager_id, source_id, name, protocol, automatic, ok_streak, collected_at) VALUES('radarr', 1, 'T', 'torrent', 1, 3, 1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v16 database: %v", err)
	}
	defer s.Close()
	if list, _ := s.Sources(ctx, "radarr"); len(list) != 1 || !list[0].StatusKnown || list[0].OKStreak != 3 {
		t.Fatalf("%+v", list)
	}
}

// Schema 17 → 18: existing missing content is ordinary ("missing"); a pack
// follow-up marks an existing row or creates one, prioritised first.
func TestForwardMigrationV17MissingOrigin(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := migrationFS.ReadDir("migrations")
	for _, e := range entries {
		if e.Name() >= "0018" {
			break
		}
		body, _ := migrationFS.ReadFile("migrations/" + e.Name())
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	for _, q := range []string{
		"INSERT INTO meta(key, value) VALUES('schema_version','17'),('data_origin','live'),('instance_id','v17inst'),('created_at','0')",
		"INSERT INTO missing_content(manager_id, content_key, title, first_seen, last_seen) VALUES('sonarr','episode:1','old',1,1)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v17 database: %v", err)
	}
	defer s.Close()
	if r, _ := s.MissingRecord(ctx, "sonarr", "episode:1"); r == nil || r.Origin != "missing" {
		t.Fatalf("%+v", r)
	}
	if err := s.Write(ctx, func(tx *Tx) error { return tx.MarkPackFollowUp("sonarr", "episode:9", "pack E09", 7, time.Unix(2, 0)) }); err != nil {
		t.Fatal(err)
	}
	list, _ := s.MissingContent(ctx, MissingFilter{})
	if len(list) != 2 || list[0].ContentKey != "episode:9" || list[0].Origin != "pack" || *list[0].OriginIntent != 7 {
		t.Fatalf("%+v", list)
	}
}

func TestForwardMigrationV18NotificationRateUsage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "0019" {
			break
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	if _, err := db.Exec("INSERT INTO meta(key, value) VALUES('schema_version','18'),('data_origin','live'),('instance_id','v18inst'),('created_at','0')"); err != nil {
		t.Fatal(err)
	}
	sentAt := time.Unix(7200, 0)
	if _, err := db.Exec(`INSERT INTO notification_events(target, dedup_key, kind, severity, title, body, created_at, next_at, sent_at)
		VALUES('hook','opening','finding.opened','warning','title','body',?,?,?)`, ms(sentAt), ms(sentAt), ms(sentAt)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v18 database: %v", err)
	}
	defer s.Close()
	if err := s.Write(ctx, func(tx *Tx) error {
		ok, err := tx.ReserveNotificationAttempt("hook", sentAt.Add(30*time.Minute), 1)
		if err != nil || ok {
			t.Fatalf("the old delivery did not count towards pacing: %t, %v", ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Schema 20: cooldowns recorded by schema 19 take their start from the
// action that set them, so a shorter configured cooldown ends them early.
func TestForwardMigrationV19CooldownSince(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, dbFile))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "0020" {
			break
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
	}
	set := time.Unix(100000, 0)
	for _, q := range []string{
		"INSERT INTO meta(key, value) VALUES('schema_version','19'),('data_origin','live'),('instance_id','v19inst'),('created_at','0')",
		fmt.Sprintf(`INSERT INTO action_intents(id, idem_key, workflow, origin, subject_json, state, policy_revision, config_revision, instance_id, fingerprint,
		  evidence_json, plan_json, review_required, created_at, updated_at) VALUES
		 (45,'k45','no_progress_recovery','automatic','{}','succeeded','p','c','v19inst','f','{}','{}',0,%d,%d)`, ms(set), ms(set)),
		fmt.Sprintf(`INSERT INTO action_cooldowns(subject, until, reason) VALUES
		 ('content:sonarr|episode:1',%d,'action #45 (no_progress_recovery)'),
		 ('content:sonarr|episode:2',%d,'something else')`, ms(set.Add(24*time.Hour)), ms(set.Add(24*time.Hour))),
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("open v19 database: %v", err)
	}
	defer s.Close()
	at := set.Add(2 * time.Hour)
	if cs, err := s.ActiveCooldowns(ctx, at, time.Hour); err != nil || len(cs) != 0 {
		t.Fatalf("with a 1h cooldown both have ended 2h after the action: %+v %v", cs, err)
	}
	cs, err := s.ActiveCooldowns(ctx, at, 0)
	if err != nil || len(cs) != 2 {
		t.Fatalf("recorded ends: %+v %v", cs, err)
	}
	if cs, _ := s.ActiveCooldowns(ctx, at, 23*time.Hour); len(cs) != 2 || !cs["content:sonarr|episode:1"].Until.Equal(set.Add(23*time.Hour)) {
		t.Fatalf("capped end: %+v", cs)
	}
}
