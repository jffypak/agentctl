// Command agentctl is a session manager for coding agents running in tmux
// panes and git worktrees. Bare `agentctl` opens the TUI; subcommands are
// listed in commands.go (one cmd_<name>.go per subcommand).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jffypak/agentctl/internal/controller"
)

// Exit codes.
const (
	exitOK    = 0
	exitError = 1 // runtime failure: one actionable line on stderr
	exitUsage = 2 // bad usage or not implemented yet
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], osSystem()))
}

// system is everything a subcommand may touch outside the process. main uses
// the real process; tests build one with a hermetic testenv.Env and call run
// in-process. Subcommands must read the environment only through it.
type system struct {
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	environ    []string
	cwd        string
	now        func() time.Time
	executable string // absolute path of this agentctl binary (hook command target)
}

func osSystem() *system {
	cwd, _ := os.Getwd()
	exe, _ := os.Executable()
	return &system{
		stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr,
		environ: os.Environ(), cwd: cwd, now: time.Now, executable: exe,
	}
}

// getenv looks a variable up in the system environment (last wins).
func (s *system) getenv(key string) string {
	for i := len(s.environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(s.environ[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// openController opens the registry and wires the controller. Commands that
// must stay light (hook) build what they need themselves instead.
func (s *system) openController(ctx context.Context) (*controller.Controller, error) {
	return controller.Open(ctx, controller.Config{
		Environ: s.environ, Now: s.now, AgentctlPath: s.executable,
	})
}

// fail prints `agentctl: <one line>` to stderr and returns exitError.
func (s *system) fail(err error) int {
	fmt.Fprintf(s.stderr, "agentctl: %s\n", controller.OneLine(err.Error()))
	return exitError
}

// usageError prints `agentctl <cmd>: <msg>` to stderr and returns exitUsage.
func (s *system) usageError(cmd, format string, args ...any) int {
	fmt.Fprintf(s.stderr, "agentctl %s: %s\n", cmd, controller.OneLine(fmt.Sprintf(format, args...)))
	return exitUsage
}
