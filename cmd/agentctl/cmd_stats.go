package main

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m2-tui-inbox.
//
// `agentctl stats [--json]`: median ui.opened -> first ui.action, runs with no action,
// attaches per prompt.
func cmdStats(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "stats", "m2-tui-inbox")
}
