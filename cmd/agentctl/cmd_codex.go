package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jffypak/agentctl/internal/controller"
)

// cmdCodex: agentctl codex [--name <name>] [--] [codex args…]
//
// Runs Codex CLI in the current tmux pane with agentctl's hooks and tracks
// it (codex-support design). The pane is registered first (origin wrapped),
// then Codex runs as a child process with the terminal passed through, and
// its exit is recorded: the pane outlives Codex (it is the user's shell), so
// only the wrapper can tell that Codex is gone. Exits with Codex's exit code.
func cmdCodex(ctx context.Context, sys *system, args []string) int {
	name, userArgs, err := parseCodexArgs(args)
	if err != nil {
		return sys.usageError("codex", "usage: agentctl codex [--name <name>] [--] [codex args...]")
	}
	pane := sys.getenv("TMUX_PANE")
	if sys.getenv("TMUX") == "" || pane == "" {
		fmt.Fprintln(sys.stderr, "agentctl: agentctl codex must run inside tmux (start tmux, then run it in a pane)")
		return exitError
	}
	// SIGINT and SIGQUIT are caught (and dropped) from before registration
	// until after the exit is recorded: a Ctrl-C at either edge must not kill
	// the wrapper between writing the session and recording its end.
	// Catching, unlike ignoring, is not inherited by the child.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)

	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	res, err := ctl.RegisterWrapped(ctx, controller.WrapRequest{Name: name, PaneID: pane, UserArgs: userArgs})
	_ = ctl.Close() // never hold the registry open while Codex runs
	if err != nil {
		return sys.fail(err)
	}
	verb := "tracking this pane as"
	if res.Reused {
		verb = "tracking this pane again as"
	}
	fmt.Fprintf(sys.stderr, "agentctl: %s %s\n", verb, res.TaskID)

	code := runChild(sys, sigs, res.Argv, append(append([]string(nil), sys.environ...), res.Env...))

	ctl, err = sys.openController(context.WithoutCancel(ctx))
	if err != nil {
		fmt.Fprintf(sys.stderr, "agentctl: could not record that codex exited: %v\n", err)
		return code
	}
	defer ctl.Close()
	if err := ctl.RecordWrappedExit(context.WithoutCancel(ctx), res.SessionID, code); err != nil {
		fmt.Fprintf(sys.stderr, "agentctl: could not record that codex exited: %v\n", err)
	}
	return code
}

// runChild runs argv with the terminal passed through and returns its exit
// code (128+signal when killed, 127 when it could not start). sigs carries
// the caught SIGINT/SIGQUIT (dropped: the terminal delivers them to Codex
// too) and SIGTERM/SIGHUP (forwarded to Codex). SIGTSTP keeps its default,
// so Ctrl-Z stops the whole job as usual.
func runChild(sys *system, sigs <-chan os.Signal, argv, env []string) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env, cmd.Dir = sys.stdin, sys.stdout, sys.stderr, env, sys.cwd
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(sys.stderr, "agentctl: cannot start codex: %v\n", err)
		return 127
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if s == syscall.SIGTERM || s == syscall.SIGHUP {
					_ = cmd.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	fmt.Fprintf(sys.stderr, "agentctl: codex: %v\n", err)
	return 1
}

// parseCodexArgs takes a leading --name <n> / --name=<n> and an optional
// "--"; everything else is passed to codex unchanged.
func parseCodexArgs(args []string) (name string, rest []string, err error) {
	for len(args) > 0 {
		switch a := args[0]; {
		case a == "--name":
			if len(args) < 2 {
				return "", nil, errors.New("--name needs a value")
			}
			name, args = args[1], args[2:]
			continue
		case strings.HasPrefix(a, "--name="):
			name, args = strings.TrimPrefix(a, "--name="), args[1:]
			continue
		case a == "--":
			args = args[1:]
		}
		break
	}
	if len(args) > 0 {
		rest = append([]string(nil), args...)
	}
	return name, rest, nil
}
