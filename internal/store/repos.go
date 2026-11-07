package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// Repo is a registered main repository (never a linked worktree).
type Repo struct {
	ID        int64
	RootPath  string // realpath of the main worktree root
	Name      string
	CreatedAt time.Time
}

// Repos is the repos repository.
type Repos struct{ s *Store }

// Repos returns the repos repository.
func (s *Store) Repos() *Repos { return &Repos{s} }

const repoCols = `id, root_path, name, created_at`

func scanRepo(r rowScanner) (Repo, error) {
	var (
		repo    Repo
		created int64
	)
	if err := r.Scan(&repo.ID, &repo.RootPath, &repo.Name, &created); err != nil {
		return Repo{}, err
	}
	repo.CreatedAt = fromMS(created)
	return repo, nil
}

// Upsert registers rootPath (absolute) or returns the existing row.
func (r *Repos) Upsert(ctx context.Context, rootPath, name string) (Repo, error) {
	if !filepath.IsAbs(rootPath) {
		return Repo{}, fmt.Errorf("store: repo root %q is not absolute", rootPath)
	}
	row := r.s.db.QueryRowContext(ctx, `INSERT INTO repos (root_path, name, created_at) VALUES (?, ?, ?)
		ON CONFLICT (root_path) DO UPDATE SET name = excluded.name
		RETURNING `+repoCols, rootPath, name, ms(r.s.now()))
	repo, err := scanRepo(row)
	if err != nil {
		return Repo{}, fmt.Errorf("store: upsert repo %s: %w", rootPath, err)
	}
	return repo, nil
}

// ByPath finds a repo by its root path. ErrNotFound if absent.
func (r *Repos) ByPath(ctx context.Context, rootPath string) (Repo, error) {
	repo, err := scanRepo(r.s.db.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repos WHERE root_path = ?`, rootPath))
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, fmt.Errorf("store: repo %s: %w", rootPath, ErrNotFound)
	}
	if err != nil {
		return Repo{}, fmt.Errorf("store: repo %s: %w", rootPath, err)
	}
	return repo, nil
}

// Get finds a repo by id. ErrNotFound if absent.
func (r *Repos) Get(ctx context.Context, id int64) (Repo, error) {
	repo, err := scanRepo(r.s.db.QueryRowContext(ctx, `SELECT `+repoCols+` FROM repos WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Repo{}, fmt.Errorf("store: repo %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Repo{}, fmt.Errorf("store: repo %d: %w", id, err)
	}
	return repo, nil
}
