package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
)

// Task is one unit of work: a worktree on its own branch plus (normally) one
// agent session.
type Task struct {
	ID           string // slug, also the display name in Phase 1
	RepoID       int64
	Name         string
	Goal         string
	State        domain.TaskState
	WorktreePath string
	Branch       string
	BaseRef      string
	BaseSHA      string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ClosedAt     time.Time // zero while running
}

// Tasks is the tasks repository. Closing lives in tasks_close.go
// (m1-pane-identity).
type Tasks struct{ s *Store }

// Tasks returns the tasks repository.
func (s *Store) Tasks() *Tasks { return &Tasks{s} }

const taskCols = `id, repo_id, name, goal, state, worktree_path, branch, base_ref, base_sha, created_at, updated_at, closed_at`

func scanTask(r rowScanner) (Task, error) {
	var (
		t                Task
		created, updated int64
		closed           sql.NullInt64
	)
	if err := r.Scan(&t.ID, &t.RepoID, &t.Name, &t.Goal, &t.State, &t.WorktreePath, &t.Branch,
		&t.BaseRef, &t.BaseSHA, &created, &updated, &closed); err != nil {
		return Task{}, err
	}
	t.CreatedAt, t.UpdatedAt, t.ClosedAt = fromMS(created), fromMS(updated), fromNullMS(closed)
	return t, nil
}

// Reserve inserts a running task row. It is the serialization point for
// concurrent `agentctl new <same name>`: exactly one INSERT wins and the rest
// get ErrTaskExists. The caller undoes a reservation with Unreserve if the
// worktree cannot be created.
func (t *Tasks) Reserve(ctx context.Context, task Task) (Task, error) {
	if err := domain.ValidateSlug(task.ID); err != nil {
		return Task{}, err
	}
	if task.WorktreePath == "" || task.Branch == "" || task.RepoID == 0 {
		return Task{}, fmt.Errorf("store: reserve %s: repo, worktree and branch are required", task.ID)
	}
	if task.Name == "" {
		task.Name = task.ID
	}
	now := t.s.now()
	task.State, task.CreatedAt, task.UpdatedAt, task.ClosedAt = domain.TaskRunning, now, now, time.Time{}
	_, err := t.s.db.ExecContext(ctx, `INSERT INTO tasks (`+taskCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		task.ID, task.RepoID, task.Name, task.Goal, task.State, task.WorktreePath, task.Branch,
		task.BaseRef, task.BaseSHA, ms(now), ms(now))
	if isConstraint(err) {
		if ok, _ := t.Exists(ctx, task.ID); ok {
			return Task{}, fmt.Errorf("store: task %s: %w", task.ID, ErrTaskExists)
		}
	}
	if err != nil {
		return Task{}, fmt.Errorf("store: reserve task %s: %w", task.ID, err)
	}
	return task, nil
}

// Unreserve deletes a task row that never got a session (compensation for a
// failed worktree add). It refuses once a session exists: from then on the
// task is history and is only ever closed.
func (t *Tasks) Unreserve(ctx context.Context, id string) error {
	return t.s.withTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE task_id = ?`, id).Scan(&n); err != nil {
			return fmt.Errorf("store: unreserve %s: %w", id, err)
		}
		if n > 0 {
			return fmt.Errorf("store: unreserve %s: %w", id, ErrTaskHasSession)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id); err != nil {
			return fmt.Errorf("store: unreserve %s: %w", id, err)
		}
		return nil
	})
}

// Get returns one task. ErrNotFound if absent.
func (t *Tasks) Get(ctx context.Context, id string) (Task, error) {
	task, err := scanTask(t.s.db.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("store: task %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("store: task %s: %w", id, err)
	}
	return task, nil
}

// Exists reports whether a task row with this id exists (running or closed).
func (t *Tasks) Exists(ctx context.Context, id string) (bool, error) {
	var n int
	if err := t.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = ?`, id).Scan(&n); err != nil {
		return false, fmt.Errorf("store: task %s: %w", id, err)
	}
	return n > 0, nil
}
