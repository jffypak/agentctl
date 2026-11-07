package main

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m2-tui-inbox.
//
// Bare `agentctl` and `agentctl tui` open the Bubble Tea v2 TUI. It holds a concrete
// *controller.Controller and never opens the DB for writing (invariant 1).
func cmdTUI(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "tui", "m2-tui-inbox")
}
