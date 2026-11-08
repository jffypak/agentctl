package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/pager"
)

// stdoutIsTerminal decides between paging and plain output. A variable so a
// (non-parallel) test can exercise the terminal path without a pty.
var stdoutIsTerminal = pager.IsTerminal

// cmdDiff: agentctl diff <name> [--stat]
//
// Shows `git diff <base_sha>` plus every untracked file as a new-file diff:
// through the pager ($AGENTCTL_PAGER, $PAGER, less -R) with color when
// stdout is a terminal, plain text otherwise. --stat prints the summary
// (committed, staged, unstaged, untracked, conflicts) instead. Read-only:
// git plumbing with GIT_OPTIONAL_LOCKS=0, never `add -N`.
func cmdDiff(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "diff")
	stat := fs.Bool("stat", false, "print the change summary instead of the diff")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 1 {
		return sys.usageError("diff", "usage: agentctl diff <name> [--stat]")
	}
	name := pos[0]
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()

	if *stat {
		s, err := ctl.DiffSummary(ctx, name)
		if err != nil {
			return sys.fail(err)
		}
		for _, l := range s.Lines(0) {
			fmt.Fprintln(sys.stdout, l)
		}
		return exitOK
	}
	if !stdoutIsTerminal(sys.stdout) {
		err := ctl.WriteFullDiff(ctx, name, sys.stdout, false)
		if err != nil && !errors.Is(err, controller.ErrOutputClosed) {
			return sys.fail(err)
		}
		return exitOK
	}
	cmd, err := ctl.FullDiffCmd(ctx, name)
	if err != nil {
		return sys.fail(err)
	}
	if c, ok := cmd.Stdin.(io.Closer); ok {
		defer c.Close()
	}
	cmd.Stdout, cmd.Stderr = sys.stdout, sys.stderr
	if err := cmd.Run(); err != nil {
		return sys.fail(fmt.Errorf("pager %q failed: %w", pager.Resolve(sys.getenv), err))
	}
	return exitOK
}
