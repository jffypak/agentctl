// Package store owns agentctl's SQLite registry: opening (WAL, busy timeout,
// BEGIN IMMEDIATE, mode 0600), migrations, and the Repos / Tasks / Sessions /
// Events repositories plus the Overview read model.
//
// Repositories are reached through accessor methods (st.Tasks(), ...) whose
// types are defined here; later cards add methods in their own files
// (<entity>_<concern>.go) and messages.go adds st.Messages(). Every write that
// changes sessions.agent_state goes through Sessions.Transition (or the
// package-internal transitionTx it wraps).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
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
	db   *sql.DB
	path string
	now  func() time.Time
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
	q := url.Values{}
	q.Set("_busy_timeout", strconv.FormatInt(busy.Milliseconds(), 10))
	q.Set("_journal_mode", "WAL")
	q.Set("_txlock", "immediate")
	q.Set("_foreign_keys", "1")
	q.Set("_synchronous", "NORMAL")
	db, err := sql.Open("sqlite", path+"?"+q.Encode())
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
	return &Store{db: db, path: path, now: now}, nil
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
func (s *Store) Close() error { return s.db.Close() }

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
// the write lock is taken up front and concurrent writers queue on the busy
// timeout instead of failing with SQLITE_BUSY on a lock upgrade.
func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
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
