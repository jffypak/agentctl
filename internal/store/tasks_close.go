package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jffypak/agentctl/internal/domain"
)

// ErrTaskClosed: the task is already closed (Phase 1 never reopens).
var ErrTaskClosed = errors.New("task already closed")

// CloseInput closes one task. StopSessionID is set only after a confirmed
// `close --stop` killed that session's tmux session: it is then recorded as
// stopped (source user) in the same transaction, unless it already reached a
// terminal state (a recorded exit status is never overwritten).
type CloseInput struct {
	TaskID        string
	StopSessionID int64
	StopDetail    string
}

// Close marks a running task closed and appends task.closed{stopped,
// session_id}. It never touches the worktree or the branch; without
// StopSessionID it never touches the session either. ErrNotFound /
// ErrTaskClosed leave everything unchanged.
func (t *Tasks) Close(ctx context.Context, in CloseInput) (Task, error) {
	var out Task
	err := t.s.withTx(ctx, func(tx *sql.Tx) error {
		task, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, in.TaskID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: task %s: %w", in.TaskID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("store: close %s: %w", in.TaskID, err)
		}
		if !domain.CanTransitionTask(task.State, domain.TaskClosed) {
			return fmt.Errorf("store: task %s: %w", in.TaskID, ErrTaskClosed)
		}
		payload := map[string]any{"stopped": in.StopSessionID != 0}
		if in.StopSessionID != 0 {
			if err := t.recordStopTx(ctx, tx, in); err != nil {
				return err
			}
			payload["session_id"] = in.StopSessionID
		}
		now := t.s.now()
		r, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'closed', closed_at = ?, updated_at = ? WHERE id = ? AND state = 'running'`,
			ms(now), ms(now), in.TaskID)
		if err != nil {
			return fmt.Errorf("store: close %s: %w", in.TaskID, err)
		}
		if n, err := r.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("store: close %s: task changed concurrently (%d rows, %v)", in.TaskID, n, err)
		}
		if _, err := appendEvent(ctx, tx, now, EventInput{Type: domain.EventTaskClosed, TaskID: in.TaskID, Payload: payload}); err != nil {
			return err
		}
		task.State, task.ClosedAt, task.UpdatedAt = domain.TaskClosed, fromMS(ms(now)), fromMS(ms(now))
		out = task
		return nil
	})
	return out, err
}

func (t *Tasks) recordStopTx(ctx context.Context, tx *sql.Tx, in CloseInput) error {
	sess, err := getSession(ctx, tx, in.StopSessionID)
	if err != nil {
		return err
	}
	if sess.TaskID != in.TaskID {
		return fmt.Errorf("store: close %s: session %d belongs to task %s", in.TaskID, sess.ID, sess.TaskID)
	}
	if sess.State.IsTerminal() {
		return nil
	}
	_, err = (&Sessions{t.s}).transitionTx(ctx, tx, transitionInput{
		SessionID: sess.ID, To: domain.StateStopped, Confidence: domain.ConfidencePane,
		Detail: in.StopDetail, Source: domain.SourceUser,
	})
	return err
}
