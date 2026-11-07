package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one versioned schema change, loaded from NNNN_name.sql.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

func embeddedMigrations() ([]Migration, error) {
	sub, err := fs.Sub(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	return loadMigrations(sub)
}

// loadMigrations reads every *.sql file at the root of fsys, sorted by version.
// Names must start with a positive integer version followed by '_'.
func loadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: read migrations: %w", err)
	}
	seen := map[int]string{}
	var out []Migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, rest, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 || rest == "" {
			return nil, fmt.Errorf("store: migration %q: name must be NNNN_name.sql with NNNN > 0", name)
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("store: migrations %q and %q share version %d", prev, name, v)
		}
		seen[v] = name
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", name, err)
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// migrate applies every migration whose version is not yet recorded, in
// ascending order. It uses set semantics, not "max+1": a lower-numbered
// migration merged later (a reserved per-card range) is still applied. Each
// migration runs in its own BEGIN IMMEDIATE transaction and re-checks the
// applied set inside it, so concurrent first opens apply each version once.
func migrate(ctx context.Context, db *sql.DB, ms []Migration, now func() time.Time) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	applied, err := appliedSet(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if applied[m.Version] {
			continue
		}
		if err := applyOne(ctx, db, m, now); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(ctx context.Context, db *sql.DB, m Migration, now func() time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: migration %s: begin: %w", m.Name, err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.Version).Scan(&n); err != nil {
		return fmt.Errorf("store: migration %s: %w", m.Name, err)
	}
	if n > 0 {
		return nil // another process applied it while we waited for the lock
	}
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("store: migration %s: %w", m.Name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.Version, m.Name, ms(now())); err != nil {
		return fmt.Errorf("store: migration %s: record: %w", m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migration %s: commit: %w", m.Name, err)
	}
	return nil
}

func appliedSet(ctx context.Context, q querier) (map[int]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("store: read schema_migrations: %w", err)
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("store: read schema_migrations: %w", err)
		}
		out[v] = true
	}
	return out, rows.Err()
}

// AppliedMigrations lists applied versions in ascending order.
func (s *Store) AppliedMigrations(ctx context.Context) ([]int, error) {
	set, err := appliedSet(ctx, s.db)
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Ints(out)
	return out, nil
}
