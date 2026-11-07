package main

import "context"

var _ = registerStub("tmux-binding", "m2-tui-inbox")

// STUB (m1-walking-skeleton) — owned and rewritten whole by m2-tui-inbox.
//
// `agentctl tmux-binding` prints
// bind-key a display-popup -E -w 80% -h 75% -d '#{pane_current_path}' '<abs agentctl>'.
func cmdTmuxBinding(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "tmux-binding", "m2-tui-inbox")
}
