package main

import (
	"context"
	"fmt"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// cmdNew: agentctl new <name> --goal "<goal>"
func cmdNew(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "new")
	goal := fs.String("goal", "", "initial prompt for the agent (one argument, never shell-parsed)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 1 {
		return sys.usageError("new", `usage: agentctl new <name> --goal "<goal>"`)
	}
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()
	res, err := ctl.NewTask(ctx, controller.NewTaskRequest{Name: pos[0], Goal: *goal, Cwd: sys.cwd})
	if err != nil {
		return sys.fail(err)
	}
	fmt.Fprintf(sys.stdout, "started %s · %s · %s · tmux %s (%s)\n",
		res.TaskID, res.Branch, domain.SanitizeTerminalText(res.Worktree), res.SessionName, res.Identity.PaneID)
	return exitOK
}
