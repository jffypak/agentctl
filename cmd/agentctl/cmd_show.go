package main

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-inspect.
//
// `agentctl show <name> [--full]` prints the task/session/identity/state block and the
// last 40 sanitized output lines (identity verified first via liveSession).
func cmdShow(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "show", "m1-inspect")
}
