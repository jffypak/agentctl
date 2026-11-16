package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jffypak/agentctl/internal/domain"
)

// DeleteInput deletes one task. Payload goes on the task.deleted event.
type DeleteInput struct {
	TaskID  string
	Payload map[string]any
}

// ArchivedTaskID is the task_id a deleted task's events keep, so the name is
// free again while the event log keeps its history. '~' is never valid in a
// slug, so it cannot collide with a live task.
func ArchivedTaskID(id string, deletedMS int64) string {
	return fmt.Sprintf("%s~%d", id, deletedMS)
}

// Delete removes a task, its sessions and its messages in one transaction,
// so its name can be used again. Its events are kept: they move to
// ArchivedTaskID, followed by one task.deleted event. Delete never touches
// git or tmux; the controller does that first. ErrNotFound changes nothing.
func (t *Tasks) Delete(ctx context.Context, in DeleteInput) (string, error) {
	var archived string
	err := t.s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, in.TaskID)); errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: task %s: %w", in.TaskID, ErrNotFound)
		} else if err != nil {
			return fmt.Errorf("store: delete %s: %w", in.TaskID, err)
		}
		now := t.s.now()
		archived = ArchivedTaskID(in.TaskID, ms(now))
		if _, err := tx.ExecContext(ctx, `UPDATE events SET task_id = ? WHERE task_id = ?`, archived, in.TaskID); err != nil {
			return fmt.Errorf("store: delete %s: %w", in.TaskID, err)
		}
		for _, q := range []string{
			`DELETE FROM messages WHERE task_id = ?`,
			`DELETE FROM sessions WHERE task_id = ?`,
			`DELETE FROM tasks WHERE id = ?`,
		} {
			if _, err := tx.ExecContext(ctx, q, in.TaskID); err != nil {
				return fmt.Errorf("store: delete %s: %w", in.TaskID, err)
			}
		}
		payload := map[string]any{"name": in.TaskID}
		for k, v := range in.Payload {
			payload[k] = v
		}
		_, err := appendEvent(ctx, tx, now, EventInput{Type: domain.EventTaskDeleted, TaskID: archived, Payload: payload})
		return err
	})
	return archived, err
}
