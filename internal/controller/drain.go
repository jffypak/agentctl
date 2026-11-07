package controller

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m2-prompt-queue.

// DrainResult reports one Drain pass for a session.
type DrainResult struct {
	MessageID int64  // the message delivered (0 if none)
	Delivered bool   // a paste happened
	Reason    string // why nothing was delivered ("agent working", "awaiting input", ...)
}

// Drain is the idempotent delivery step shared by the TUI tick and
// `agentctl deliver`: sweep stale delivering/sent to unconfirmed, claim the
// oldest queued message with one conditional UPDATE (one delivery per ready
// transition), re-verify state and identity via liveSession, paste through
// the adapter, mark sent.
//
// The stub delivers nothing.
func (c *Controller) Drain(ctx context.Context, sessionID int64) (DrainResult, error) {
	return DrainResult{}, nil
}

// DrainAll runs Drain for every session with queued messages (TUI tick).
//
// The stub does nothing.
func (c *Controller) DrainAll(ctx context.Context) error {
	return nil
}
