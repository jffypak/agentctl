package main

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-hook-state.
//
// `agentctl hook` is spawned by Claude Code per hook event. It must always exit 0,
// write nothing to stdout, finish within HookBudget, and log errors to logs/hook-errors.log.
// It should not call sys.openController (too heavy); it opens the store with
// store.HookBusyTimeout through internal/hookingest.
func cmdHook(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "hook", "m1-hook-state")
}
