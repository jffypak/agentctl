package main

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-inspect.
//
// `agentctl diff <name> [--stat]` pipes `git diff <base_sha>` plus untracked files to the pager.
func cmdDiff(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "diff", "m1-inspect")
}
