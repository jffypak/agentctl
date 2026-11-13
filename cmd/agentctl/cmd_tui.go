package main

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/tui"
)

// cmdTUI: `agentctl` or `agentctl tui` opens the attention-sorted TUI. It
// holds a concrete *controller.Controller and never opens the registry for
// writing itself (invariant 1). Quitting (q, esc, ctrl+c) or a SIGHUP (the
// popup or terminal closed) ends only this process: agents live in their tmux
// sessions (invariant 2). A registry that cannot be opened is shown as a
// banner rather than a crash; the command then exits 1 with the same line.
// --stay keeps the TUI running after an attach moves the user's client to
// an agent, so it can live in its own pane as a dashboard. --split (implies
// --stay) opens the agent in a pane beside the TUI, in the TUI's own window;
// on exit it sends docked agents back to their sessions.
func cmdTUI(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "tui")
	stay := fs.Bool("stay", false, "keep running after attaching to an agent (for a dashboard pane)")
	split := fs.Bool("split", false, "open agents in a pane beside the TUI (implies --stay)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 0 {
		return sys.usageError("tui", "usage: agentctl [tui] [--stay] [--split]")
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP)
	defer stop()

	opts := tui.Options{
		Cwd: sys.cwd, NoColor: sys.getenv("NO_COLOR") != "", Stay: *stay, Split: *split,
		Input: sys.stdin, Output: sys.stdout, Environ: sys.environ,
	}
	if binding, err := controller.TmuxBinding(sys.executable); err == nil {
		opts.Binding = binding
	} else {
		opts.Warning = err.Error()
	}
	ctl, openErr := sys.openController(ctx)
	if openErr != nil {
		opts.OpenErr = openErr
	} else {
		defer ctl.Close()
		opts.Controller = ctl
	}
	// config.yaml keys: (m2-tui-manage); a bad file is a banner, never a crash.
	opts.Keys, opts.Warning = tui.ConfigKeyMap(sys.getenv, opts.Warning)

	runErr := tui.Run(ctx, opts)
	if *split && opts.Controller != nil {
		// the TUI's pane may be closing (SIGHUP cancelled ctx): a fresh,
		// short context still gets the agents home
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		if err := opts.Controller.Undock(uctx); err != nil && runErr == nil {
			fmt.Fprintln(sys.stderr, "agentctl tui: could not move docked agents back: "+err.Error())
		}
		cancel()
	}
	if runErr != nil {
		return sys.fail(runErr)
	}
	if openErr != nil {
		return sys.fail(openErr)
	}
	return exitOK
}
