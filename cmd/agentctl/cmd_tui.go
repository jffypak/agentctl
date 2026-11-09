package main

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/tui"
)

// cmdTUI: `agentctl` or `agentctl tui` opens the attention-sorted TUI. It
// holds a concrete *controller.Controller and never opens the registry for
// writing itself (invariant 1). Quitting (q, esc, ctrl+c) or a SIGHUP (the
// popup or terminal closed) ends only this process: agents live in their tmux
// sessions (invariant 2). A registry that cannot be opened is shown as a
// banner rather than a crash; the command then exits 1 with the same line.
func cmdTUI(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "tui")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 0 {
		return sys.usageError("tui", "usage: agentctl [tui]")
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP)
	defer stop()

	opts := tui.Options{
		Cwd: sys.cwd, NoColor: sys.getenv("NO_COLOR") != "",
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

	if err := tui.Run(ctx, opts); err != nil {
		return sys.fail(err)
	}
	if openErr != nil {
		return sys.fail(openErr)
	}
	return exitOK
}
