package controller

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-pane-identity.

// Reconcile verifies every active session against tmux: one snapshot per
// socket (c.snapshot), livenessFrom per session, StartingTimeout for starting
// sessions with no hook, and store transitions with source pane/timeout plus
// session.disconnected{reason}. It must be idempotent and never treat
// LivenessUnverified as a change. `agentctl ls` calls it before Overview; the
// TUI calls it every Tunables.Reconcile.
//
// The stub does nothing.
func (c *Controller) Reconcile(ctx context.Context) error {
	return nil
}
