package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/jffypak/agentctl/internal/controller"
)

const closeUsage = "usage: agentctl close <name> [--stop [--yes]]"

// cmdClose: `agentctl close <name> [--stop [--yes]]` marks the task closed
// (hidden from ls, shown with --closed); the worktree and branch are never
// touched. --stop also kills the tmux session, only for a session agentctl
// launched whose pane identity verifies now, and only after an interactive
// "y" (or --yes).
func cmdClose(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "close")
	stop := fs.Bool("stop", false, "also kill the task's tmux session (launched sessions only; asks first)")
	yes := fs.Bool("yes", false, "with --stop: do not ask for confirmation")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 1 || (*yes && !*stop) {
		return sys.usageError("close", "%s", closeUsage)
	}
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()
	req := controller.CloseRequest{Name: pos[0], Stop: *stop}
	if *stop && !*yes {
		plan, err := ctl.PlanClose(ctx, req)
		if err != nil {
			return sys.fail(err)
		}
		fmt.Fprintf(sys.stderr, "Stop tmux session %s (%s) and close %s? The worktree and branch are kept. [y/N] ",
			clean(plan.SessionName), plan.Identity.PaneID, plan.TaskID)
		if !confirmed(sys) {
			fmt.Fprintln(sys.stderr, "agentctl: close --stop aborted; nothing changed")
			return exitError
		}
	}
	res, err := ctl.CloseTask(ctx, req)
	if err != nil {
		return sys.fail(err)
	}
	fmt.Fprintln(sys.stdout, closeSummary(res))
	return exitOK
}

// confirmed reads one answer line; only "y" or "yes" (any case) confirms.
// EOF or an empty line is No.
func confirmed(sys *system) bool {
	line, _ := bufio.NewReader(sys.stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func closeSummary(res controller.CloseResult) string {
	parts := []string{"closed " + res.TaskID}
	switch {
	case res.Stopped:
		parts[0] = "stopped tmux session " + clean(res.SessionName) + " and closed " + res.TaskID
	case res.HasSession:
		parts = append(parts, "tmux session "+clean(res.SessionName)+" untouched")
	}
	parts = append(parts, "worktree kept at "+clean(res.Worktree), "branch "+clean(res.Branch)+" kept")
	return strings.Join(parts, " · ")
}
