package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/jffypak/agentctl/internal/controller"
)

const closeUsage = "usage: agentctl close <name> [--stop | --delete] [--yes]"

// cmdClose: `agentctl close <name> [--stop [--yes]]` marks the task closed
// (hidden from ls, shown with --closed); the worktree and branch are never
// touched. --stop also kills the tmux session, only for a session agentctl
// launched whose pane identity verifies now, and only after an interactive
// "y" (or --yes). --delete removes the task so its name is free again; for a
// task `agentctl new` created it also stops the agent, removes the worktree
// and deletes the branch. It works on closed tasks too and always asks first
// (or --yes).
func cmdClose(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "close")
	stop := fs.Bool("stop", false, "also kill the task's tmux session (launched sessions only; asks first)")
	del := fs.Bool("delete", false, "delete the task: stop its agent, remove its worktree and branch, free its name (asks first)")
	yes := fs.Bool("yes", false, "with --stop or --delete: do not ask for confirmation")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 1 || (*yes && !*stop && !*del) || (*stop && *del) {
		return sys.usageError("close", "%s", closeUsage)
	}
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()
	if *del {
		return closeDelete(ctx, sys, ctl, pos[0], *yes)
	}
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

func closeDelete(ctx context.Context, sys *system, ctl *controller.Controller, name string, yes bool) int {
	if !yes {
		plan, err := ctl.PlanDelete(ctx, name)
		if err != nil {
			return sys.fail(err)
		}
		fmt.Fprintf(sys.stderr, "Delete task %s?\n", plan.TaskID)
		for _, l := range controller.DeletePlanLines(plan) {
			fmt.Fprintf(sys.stderr, "  %s\n", clean(l))
		}
		fmt.Fprint(sys.stderr, "This cannot be undone. [y/N] ")
		if !confirmed(sys) {
			fmt.Fprintln(sys.stderr, "agentctl: close --delete aborted; nothing changed")
			return exitError
		}
	}
	res, err := ctl.DeleteTask(ctx, name)
	if err != nil {
		return sys.fail(err)
	}
	fmt.Fprintln(sys.stdout, clean(controller.DeleteSummary(res)))
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
