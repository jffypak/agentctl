package main

import "context"

var _ = registerStub("hooks-snippet", "m1-hook-state")

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-hook-state.
//
// `agentctl hooks-snippet` prints the generated hooks JSON for users to merge into their
// own Claude settings (how adopted panes get hooks).
func cmdHooksSnippet(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "hooks-snippet", "m1-hook-state")
}
