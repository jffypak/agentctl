package store

import (
	"context"
	"fmt"
)

// MarkViewed records that the user looked at the task now (tasks.viewed_at).
// A turn that ended before it is no longer unread. It never moves the time
// backwards.
func (t *Tasks) MarkViewed(ctx context.Context, id string) error {
	now := ms(t.s.now())
	res, err := t.s.db.ExecContext(ctx,
		`UPDATE tasks SET viewed_at = MAX(COALESCE(viewed_at, 0), ?) WHERE id = ?`, now, id)
	if err != nil {
		return fmt.Errorf("store: task %s viewed_at: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
