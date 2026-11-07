package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// UI actions recorded as ui.action{ui_run_id, action, task_id}
// (ARCHITECTURE.md §Instrumentation). The TUI records one when the user's
// action actually happens (an attach that switched or exec'd, a prompt that
// was queued, ...), never for a refused or stubbed key.
const (
	UIActionAttach = "attach"
	UIActionPrompt = "prompt"
	UIActionDiff   = "diff"
	UIActionLogs   = "logs"
	UIActionNew    = "new"
	UIActionAdopt  = "adopt"
	UIActionClose  = "close"
)

// UIActions lists every recordable action.
func UIActions() []string {
	return []string{UIActionAttach, UIActionPrompt, UIActionDiff, UIActionLogs, UIActionNew, UIActionAdopt, UIActionClose}
}

// UIOpened records ui.opened for a new TUI run and returns its ui_run_id.
// Every later UIAction / UIClosed of the run carries the same id, which is
// how `agentctl stats` pairs an open with its first action.
func (c *Controller) UIOpened(ctx context.Context) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ui run id: %w", err)
	}
	runID := hex.EncodeToString(b[:])
	if _, err := c.store.Events().Append(ctx, store.EventInput{
		Type: domain.EventUIOpened, Payload: map[string]string{"ui_run_id": runID},
	}); err != nil {
		return "", err
	}
	return runID, nil
}

// UIAction records ui.action{ui_run_id, action, task_id}.
func (c *Controller) UIAction(ctx context.Context, runID, action, taskID string) error {
	if runID == "" {
		return errors.New("ui.action: empty ui_run_id")
	}
	if !slices.Contains(UIActions(), action) {
		return fmt.Errorf("ui.action: unknown action %q", action)
	}
	_, err := c.store.Events().Append(ctx, store.EventInput{
		Type: domain.EventUIAction, TaskID: taskID,
		Payload: map[string]string{"ui_run_id": runID, "action": action, "task_id": taskID},
	})
	return err
}

// UIClosed records ui.closed for runID.
func (c *Controller) UIClosed(ctx context.Context, runID string) error {
	if runID == "" {
		return errors.New("ui.closed: empty ui_run_id")
	}
	_, err := c.store.Events().Append(ctx, store.EventInput{
		Type: domain.EventUIClosed, Payload: map[string]string{"ui_run_id": runID},
	})
	return err
}
