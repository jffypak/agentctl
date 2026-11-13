package controller

import (
	"context"
	"errors"

	"github.com/jffypak/agentctl/internal/store"
)

// MarkViewed records that the user looked at the task now, which clears its
// unread mark (domain.IsUnread). The TUI calls it when the user opens the
// detail or the logs, sends a prompt, or attaches. It writes no event, so a
// caller that wants the list to show the change reloads the Overview.
func (c *Controller) MarkViewed(ctx context.Context, taskID string) error {
	err := c.store.Tasks().MarkViewed(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return userErr(ErrTaskNotFound, err, "task %q not found", taskID)
	}
	return err
}
