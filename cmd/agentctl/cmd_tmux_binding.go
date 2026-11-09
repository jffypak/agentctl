package main

import (
	"context"
	"fmt"

	"github.com/jffypak/agentctl/internal/controller"
)

// cmdTmuxBinding: `agentctl tmux-binding` prints the tmux.conf line that
// opens agentctl in a popup over the current pane's directory:
//
//	bind-key a display-popup -E -w 80% -h 75% -d '#{pane_current_path}' '<abs agentctl>'
//
// The TUI empty state shows the same string (both come from
// controller.TmuxBinding of this binary's path).
func cmdTmuxBinding(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "tmux-binding")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 0 {
		return sys.usageError("tmux-binding", "usage: agentctl tmux-binding")
	}
	line, err := controller.TmuxBinding(sys.executable)
	if err != nil {
		return sys.fail(err)
	}
	fmt.Fprintln(sys.stdout, line)
	return exitOK
}
