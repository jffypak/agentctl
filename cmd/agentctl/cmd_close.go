package main

import "context"

var _ = registerStub("close", "m1-pane-identity")

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-pane-identity.
//
// `agentctl close <name> [--stop [--yes]]` marks the task closed; the worktree and branch
// are never touched. --stop kills only a launched, identity-verified session.
func cmdClose(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "close", "m1-pane-identity")
}
