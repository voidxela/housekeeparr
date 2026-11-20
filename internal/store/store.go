// Package store is Housekeeparr's single embedded state store (SQLite via a
// pure-Go driver). Exactly one process may open it for writing, enforced by
// an operating-system file lock; other processes may open it read-only.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Origin distinguishes real observations from synthetic demo data. A store
// created for one origin can never be opened as the other.
type Origin string

const (
	OriginLive Origin = "live"
	OriginDemo Origin = "demo"
)

const dbFile = "housekeeparr.db"

var (
	// ErrNoState means no database exists yet in read-only mode.
	ErrNoState = errors.New("no state database found; run Housekeeparr at least once (housekeeparr run --once)")
	// ErrReadOnly is returned by writes on a read-only store.
	ErrReadOnly = errors.New("state store is open read-only")
	// ErrNewerSchema means the database was written by a newer build.
	ErrNewerSchema = errors.New("state database was created by a newer Housekeeparr version")
)

// OriginMismatchError is returned when opening demo state as live or vice versa.
type OriginMismatchError struct{ Want, Have Origin }

func (e *OriginMismatchError) Error() string {
	return fmt.Sprintf("state directory contains %s data but %s was requested; use a separate state directory", e.Have, e.Want)
}

// Options control how the store is opened.
type Options struct {
	ReadOnly bool
	Origin   Origin
	// Now supplies the clock for metadata timestamps (tests/demo).
	Now func() time.Time
}

// Store is an open state store.
type Store struct {
	dir      string
	path     string
	w        *sql.DB // single-connection writer; nil when read-only
	r        *sql.DB // read pool
	lock     *Lock
	origin   Origin
	instance string
	readOnly bool
	warnings []string
	now      func() time.Time

	execs atomic.Int64

	faultMu    sync.Mutex
	fault      error
	faultAfter int // writes still allowed before fault applies
	faultTimes int // writes that fail before fault clears; 0 = until cleared

	// confirmed caches a fingerprint of complete transfers whose stored row
	// is known to match (writer process only). It lets unchanged seeding
	// torrents skip serialization entirely; it is rebuilt after restart by a
	// full comparison.
	confirmed map[string]uint64
}

// InjectWriteFault makes every subsequent Write fail with err (nil clears
// it). It exists for fault-injection tests and operator drills of the
// "state store unwritable" path; production code never calls it.
func (s *Store) InjectWriteFault(err error) {
	s.InjectWriteFaultAfter(0, err)
}

// InjectWriteFaultAfter lets the next n writes commit, then makes every
// later Write fail with err, so tests can fail one exact journal commit.
func (s *Store) InjectWriteFaultAfter(n int, err error) {
	s.InjectWriteFaults(n, 0, err)
}

// InjectWriteFaults lets the next skip writes commit, then fails the
// following times writes (0: every later write until cleared) with err: a
// transient fault when times > 0.
func (s *Store) InjectWriteFaults(skip, times int, err error) {
	s.faultMu.Lock()
	s.fault, s.faultAfter, s.faultTimes = err, skip, times
	s.faultMu.Unlock()
}

// Open opens (creating if necessary) the store in dir.
func Open(ctx context.Context, dir string, opts Options) (*Store, error) {
	if opts.Origin == "" {
		opts.Origin = OriginLive
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Store{dir: dir, path: filepath.Join(dir, dbFile), origin: opts.Origin, readOnly: opts.ReadOnly, now: opts.Now}
	if opts.ReadOnly {
		if _, err := os.Stat(s.path); errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoState
		}
		r, err := sql.Open("sqlite", dsn(s.path, true))
		if err != nil {
			return nil, err
		}
		r.SetMaxOpenConns(2)
		s.r = r
		if err := s.checkMeta(ctx); err != nil {
			r.Close()
			return nil, err
		}
		return s, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if w := networkFilesystemWarning(dir); w != "" {
		s.warnings = append(s.warnings, w)
	}
	lock, err := AcquireLock(filepath.Join(dir, "housekeeparr.lock"))
	if err != nil {
		return nil, err
	}
	s.lock = lock
	fail := func(err error) (*Store, error) {
		s.Close()
		return nil, err
	}
	w, err := sql.Open("sqlite", dsn(s.path, false))
	if err != nil {
		return fail(err)
	}
	w.SetMaxOpenConns(1)
	s.w = w
	if err := quickCheck(ctx, w); err != nil {
		return fail(err)
	}
	if err := s.migrate(ctx); err != nil {
		return fail(err)
	}
	if err := s.initMeta(ctx); err != nil {
		return fail(err)
	}
	r, err := sql.Open("sqlite", dsn(s.path, true))
	if err != nil {
		return fail(err)
	}
	r.SetMaxOpenConns(4)
	s.r = r
	return s, nil
}

func dsn(path string, readOnly bool) string {
	q := "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	if readOnly {
		return "file:" + path + "?mode=ro&" + q + "&_pragma=query_only(1)"
	}
	return "file:" + path + "?" + q + "&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_txlock=immediate"
}

func quickCheck(ctx context.Context, db *sql.DB) error {
	var res string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&res); err != nil {
		return fmt.Errorf("state database integrity check failed: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("state database is corrupt (%s); restore a backup from the state directory's backups folder", res)
	}
	return nil
}

// Close releases the database and the ownership lock.
func (s *Store) Close() error {
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	if s.w != nil {
		errs = append(errs, s.w.Close())
	}
	if s.lock != nil {
		errs = append(errs, s.lock.Release())
	}
	return errors.Join(errs...)
}

// Dir returns the state directory.
func (s *Store) Dir() string { return s.dir }

// Origin returns the data origin of this store.
func (s *Store) Origin() Origin { return s.origin }

// InstanceID returns the persistent instance identifier.
func (s *Store) InstanceID() string { return s.instance }

// ReadOnly reports whether writes are disallowed.
func (s *Store) ReadOnly() bool { return s.readOnly }

// Warnings returns non-fatal concerns about the state location.
func (s *Store) Warnings() []string { return s.warnings }

func migrations() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, nil
}

// SchemaVersion returns the latest schema version this build knows.
func SchemaVersion() int {
	names, _ := migrations()
	return len(names)
}

func (s *Store) currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='meta'").Scan(&n)
	if err != nil || n == 0 {
		return 0, err
	}
	var v string
	err = db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='schema_version'").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}

func (s *Store) migrate(ctx context.Context) error {
	names, err := migrations()
	if err != nil {
		return err
	}
	cur, err := s.currentVersion(ctx, s.w)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if cur > len(names) {
		return fmt.Errorf("%w (schema %d, this build supports %d); upgrade Housekeeparr or restore a matching backup", ErrNewerSchema, cur, len(names))
	}
	if cur == len(names) {
		return nil
	}
	if cur > 0 {
		if _, err := s.Backup(ctx, fmt.Sprintf("pre-migration-v%d", cur)); err != nil {
			return fmt.Errorf("backup before migration failed; not migrating: %w", err)
		}
	}
	for i := cur; i < len(names); i++ {
		body, err := migrationFS.ReadFile("migrations/" + names[i])
		if err != nil {
			return err
		}
		tx, err := s.w.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", names[i], err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key, value) VALUES('schema_version', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", strconv.Itoa(i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Backup writes a consistent copy of the database into dir/backups and
// returns its path. Old backups beyond the newest five are removed.
func (s *Store) Backup(ctx context.Context, label string) (string, error) {
	if s.w == nil {
		return "", ErrReadOnly
	}
	bdir := filepath.Join(s.dir, "backups")
	if err := os.MkdirAll(bdir, 0o700); err != nil {
		return "", err
	}
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	name := fmt.Sprintf("housekeeparr-%s-%s-%s.db", s.now().UTC().Format("20060102T150405.000Z"), label, hex.EncodeToString(suffix))
	dst := filepath.Join(bdir, name)
	if _, err := s.w.ExecContext(ctx, "VACUUM INTO ?", dst); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(bdir)
	var olds []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "housekeeparr-") && strings.HasSuffix(e.Name(), ".db") {
			olds = append(olds, e.Name())
		}
	}
	sort.Strings(olds)
	for len(olds) > 5 {
		os.Remove(filepath.Join(bdir, olds[0]))
		olds = olds[1:]
	}
	return dst, nil
}

func (s *Store) initMeta(ctx context.Context) error {
	get := func(k string) (string, error) {
		var v string
		err := s.w.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", k).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return v, err
	}
	origin, err := get("data_origin")
	if err != nil {
		return err
	}
	if origin == "" {
		buf := make([]byte, 8)
		_, _ = rand.Read(buf)
		id := hex.EncodeToString(buf)
		now := strconv.FormatInt(ms(s.now()), 10)
		for k, v := range map[string]string{"data_origin": string(s.origin), "instance_id": id, "created_at": now} {
			if _, err := s.w.ExecContext(ctx, "INSERT INTO meta(key, value) VALUES(?, ?)", k, v); err != nil {
				return err
			}
		}
		origin = string(s.origin)
	}
	if Origin(origin) != s.origin {
		return &OriginMismatchError{Want: s.origin, Have: Origin(origin)}
	}
	s.instance, err = get("instance_id")
	return err
}

func (s *Store) checkMeta(ctx context.Context) error {
	v, err := s.currentVersion(ctx, s.r)
	if err != nil {
		return err
	}
	if v == 0 {
		return ErrNoState
	}
	if v > SchemaVersion() {
		return fmt.Errorf("%w (schema %d)", ErrNewerSchema, v)
	}
	var origin string
	if err := s.r.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='data_origin'").Scan(&origin); err != nil {
		return err
	}
	// Read-only callers accept either origin but must display it.
	s.origin = Origin(origin)
	return s.r.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='instance_id'").Scan(&s.instance)
}

// Tx is a write transaction.
type Tx struct {
	tx        *sql.Tx
	ctx       context.Context
	confirmed map[string]uint64
	pending   map[string]uint64
	execs     *atomic.Int64
}

// Write runs fn in a single write transaction.
func (s *Store) Write(ctx context.Context, fn func(*Tx) error) error {
	if s.w == nil {
		return ErrReadOnly
	}
	s.faultMu.Lock()
	fault := s.fault
	switch {
	case fault != nil && s.faultAfter > 0:
		s.faultAfter--
		fault = nil
	case fault != nil && s.faultTimes > 0:
		if s.faultTimes--; s.faultTimes == 0 {
			s.fault = nil
		}
	}
	s.faultMu.Unlock()
	if fault != nil {
		return fmt.Errorf("state store: %w", fault)
	}
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state store: begin: %w", err)
	}
	if s.confirmed == nil {
		s.confirmed = map[string]uint64{}
	}
	wtx := &Tx{tx: tx, ctx: ctx, confirmed: s.confirmed, pending: map[string]uint64{}, execs: &s.execs}
	if err := fn(wtx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		// A failed commit may have lost confirmed rows: forget the cache.
		clear(s.confirmed)
		return fmt.Errorf("state store: commit: %w", err)
	}
	// Only committed rows become confirmed; 0 marks a removal.
	for k, v := range wtx.pending {
		if v == 0 {
			delete(s.confirmed, k)
		} else {
			s.confirmed[k] = v
		}
	}
	return nil
}

func (t *Tx) exec(q string, args ...any) (sql.Result, error) {
	if t.execs != nil {
		t.execs.Add(1)
	}
	return t.tx.ExecContext(t.ctx, q, args...)
}

// Statements returns how many write statements this store has executed
// (for measurements of write volume).
func (s *Store) Statements() int64 { return s.execs.Load() }
func (t *Tx) query(q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(t.ctx, q, args...)
}
func (t *Tx) queryRow(q string, args ...any) *sql.Row { return t.tx.QueryRowContext(t.ctx, q, args...) }

// ms converts a time to Unix milliseconds.
func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullMS(t *time.Time) sql.NullInt64 {
	if t == nil || t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: ms(*t), Valid: true}
}

func ptrMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
