// Package store owns agentctl's SQLite registry: opening (WAL, busy timeout,
// BEGIN IMMEDIATE, mode 0600), migrations, and the Repos / Tasks / Sessions /
// Events repositories plus the Overview read model.
//
// Repositories are reached through accessor methods (st.Tasks(), ...) whose
// types are defined here; later cards add methods in their own files
// (<entity>_<concern>.go) and messages.go adds st.Messages(). Every write that
// changes sessions.agent_state goes through Sessions.Transition (or the
// package-internal transitionTx it wraps). Every write the store makes runs
// in withTx, which takes the write lock fairly (see beginWrite).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DefaultBusyTimeout is how long a writer waits for the write lock. The hook
// process passes HookBusyTimeout to stay inside its 2s budget.
const (
	DefaultBusyTimeout = 5 * time.Second
	HookBusyTimeout    = 1500 * time.Millisecond
)

// Write-lock acquisition (beginWrite; docs/notes/m1-flaky-tests.md).
const (
	// writeRetry is the mean pause between BEGIN IMMEDIATE attempts while
	// another connection holds the write lock. Each pause is jittered to
	// 50–150% so waiters do not probe in lockstep.
	writeRetry = time.Millisecond
	// writeYield is how long a Store leaves the write lock alone after
	// releasing it, so a writer that is already waiting gets the next turn
	// instead of being overtaken by this Store's next write. It exceeds the
	// longest writeRetry pause.
	writeYield = 2 * time.Millisecond
)

// Sentinel errors. Callers use errors.Is.
var (
	ErrNotFound             = errors.New("not found")
	ErrTaskExists           = errors.New("task already exists")
	ErrTaskHasSession       = errors.New("task already has a session")
	ErrTransitionNotAllowed = errors.New("agent state transition not allowed")
)

// Options configure Open. The zero value is the TUI/CLI default.
type Options struct {
	BusyTimeout time.Duration    // default DefaultBusyTimeout
	Now         func() time.Time // default time.Now; tests inject a fixed clock
}

// Store is an open registry. It is safe for concurrent use.
type Store struct {
	db   *sql.DB // reads, migrations, fixtures: SQLite busy_timeout = busy
	wdb  *sql.DB // write transactions (withTx): no busy handler, beginWrite waits
	path string
	now  func() time.Time
	busy time.Duration

	epoch    time.Time    // monotonic base for released
	released atomic.Int64 // ns since epoch at the last write-lock release; 0 = never
}

// Open opens (creating if needed) the database at path with mode 0600, WAL,
// the busy timeout, foreign keys, and BEGIN IMMEDIATE for every transaction,
// then applies all unapplied embedded migrations. The parent directory must
// exist (see paths.EnsureHome).
func Open(ctx context.Context, path string, opt Options) (*Store, error) {
	if path == "" || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("store: invalid database path %q", path)
	}
	if err := ensurePrivateFile(path); err != nil {
		return nil, err
	}
	busy := opt.BusyTimeout
	if busy <= 0 {
		busy = DefaultBusyTimeout
	}
	db, err := sql.Open("sqlite", dsn(path, busy))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	ms, err := embeddedMigrations()
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db, ms, now); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Write connections open lazily inside beginWrite, which retries a busy
	// open (PRAGMA journal_mode reads the database) like a busy BEGIN.
	wdb, err := sql.Open("sqlite", dsn(path, 0))
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return &Store{db: db, wdb: wdb, path: path, now: now, busy: busy, epoch: time.Now()}, nil
}

// dsn is the modernc DSN for path. busy 0 installs no busy handler: a locked
// database fails at once with SQLITE_BUSY.
func dsn(path string, busy time.Duration) string {
	q := url.Values{}
	q.Set("_busy_timeout", strconv.FormatInt(busy.Milliseconds(), 10))
	q.Set("_journal_mode", "WAL")
	q.Set("_txlock", "immediate")
	q.Set("_foreign_keys", "1")
	q.Set("_synchronous", "NORMAL")
	return path + "?" + q.Encode()
}

func ensurePrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("store: create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: create %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("store: chmod %s: %w", path, err)
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return errors.Join(s.wdb.Close(), s.db.Close()) }

// Path is the database file path.
func (s *Store) Path() string { return s.path }

// DB exposes the handle for tests and fixtures (e.g. seeding messages before
// store/messages.go exists). Production writes go through the repositories.
func (s *Store) DB() *sql.DB { return s.db }

// Now is the store's clock.
func (s *Store) Now() time.Time { return s.now() }

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// withTx runs fn in a BEGIN IMMEDIATE transaction (the DSN sets _txlock), so
// the write lock is taken up front (by beginWrite) and is never upgraded from
// a read snapshot. It is the only way the store writes.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { s.released.Store(int64(time.Since(s.epoch)) + 1) }() // after COMMIT/ROLLBACK
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// beginWrite takes the write lock with BEGIN IMMEDIATE, waiting at most the
// Store's busy timeout. It does the waiting itself instead of leaving it to
// SQLite's busy handler, which backs off to 100 ms sleeps: that is about 25
// probes in the hook's 1.5 s, and a writer that just committed takes the lock
// again at once, so while others write back to back a waiter can miss every
// moment the lock is free and fail with SQLITE_BUSY even though the lock
// changed hands hundreds of times. Here a busy attempt (any SQLITE_BUSY_*
// code, including a busy connection open) is retried every writeRetry, and a
// Store that just released the lock waits writeYield before taking it again,
// which leaves a waiting writer a turn.
func (s *Store) beginWrite(ctx context.Context) (*sql.Tx, error) {
	deadline := time.Now().Add(s.busy)
	for {
		if wait := s.yieldLeft(); wait > 0 {
			if err := sleepCtx(ctx, min(wait, time.Until(deadline))); err != nil {
				return nil, err
			}
		}
		tx, err := s.wdb.BeginTx(ctx, nil)
		if err == nil || !isBusy(err) {
			return tx, err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return nil, err
		}
		pause := writeRetry/2 + rand.N(writeRetry)
		if err := sleepCtx(ctx, min(pause, left)); err != nil {
			return nil, err
		}
	}
}

// yieldLeft is how much of writeYield remains since this Store last released
// the write lock.
func (s *Store) yieldLeft() time.Duration {
	at := s.released.Load()
	if at == 0 {
		return 0
	}
	return writeYield - (time.Since(s.epoch) - time.Duration(at-1))
}

// sleepCtx sleeps for d (nothing when d <= 0) or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// isBusy reports whether err is SQLITE_BUSY or one of its extended codes
// (BUSY_RECOVERY, BUSY_SNAPSHOT, BUSY_TIMEOUT).
func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}

// ms converts a time to stored unix milliseconds.
func ms(t time.Time) int64 { return t.UnixMilli() }

// nullMS stores the zero time as NULL.
func nullMS(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

// fromMS converts stored unix milliseconds back to UTC time.
func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromNullMS(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return fromMS(v.Int64)
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// isConstraint reports whether err is a SQLite constraint violation
// (PRIMARY KEY / UNIQUE / CHECK / FOREIGN KEY).
func isConstraint(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}
