package claude

import (
	"context"
	"errors"
	"fmt"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
)

// ErrStopRefused: the handle's pane identity could not be verified (pane
// gone, %N reused, server restarted, or the probe failed), so nothing was
// killed.
var ErrStopRefused = errors.New("stop refused: pane identity not verified")

// Stop kills the handle's tmux session. It is only called for an explicit,
// confirmed `close --stop` on a launched session (invariant 2); the
// controller checks origin and identity first, and Stop checks again:
//
//  1. Observe must verify the identity (alive or dead), else ErrStopRefused;
//  2. the kill itself is tmux.KillVerifiedSession, which re-checks server pid,
//     session id, pane id and pane pid inside the server, so a %N reused in
//     between is never killed;
//  3. a fresh Observe must no longer verify the identity.
func (a *Adapter) Stop(ctx context.Context, h adapter.Handle) error {
	before, err := a.Observe(ctx, h)
	if err != nil {
		return err
	}
	if !before.Liveness.IsVerified() {
		return fmt.Errorf("claude: tmux session %s (%s): %s: %w", h.SessionName, h.Identity.PaneID, before.Liveness, ErrStopRefused)
	}
	if err := a.client(h.Identity.Socket).KillVerifiedSession(ctx, h.Identity); err != nil {
		return fmt.Errorf("claude: stop tmux session %s (%s): %w", h.SessionName, h.Identity.PaneID, err)
	}
	after, err := a.Observe(ctx, h)
	if err != nil {
		return err
	}
	if after.Liveness.IsVerified() {
		return fmt.Errorf("claude: tmux session %s (%s) is still there after the kill", h.SessionName, h.Identity.PaneID)
	}
	if after.Liveness.Status == domain.LivenessUnverified {
		return fmt.Errorf("claude: could not confirm tmux session %s (%s) stopped: %s", h.SessionName, h.Identity.PaneID, after.Liveness)
	}
	return nil
}
