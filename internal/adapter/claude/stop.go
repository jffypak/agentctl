package claude

import (
	"context"

	"github.com/jffypak/agentctl/internal/adapter"
)

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-pane-identity.

// Stop kills the handle's tmux session. Only called for an explicit,
// confirmed `close --stop` on a launched session whose identity was verified
// immediately before (invariant 2). Uses the kill builders in tmux/kill.go.
func (a *Adapter) Stop(ctx context.Context, h adapter.Handle) error {
	return adapter.ErrNotImplemented
}
