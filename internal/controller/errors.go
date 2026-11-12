package controller

import (
	"errors"
	"fmt"
	"strings"
)

// Error kinds. Use errors.Is(err, controller.ErrX); the *Error itself carries
// the one-line, actionable message the CLI prints and the TUI shows in a banner.
var (
	ErrInvalidName     = errors.New("invalid task name")
	ErrInvalidGoal     = errors.New("invalid goal")
	ErrAgentCmdInvalid = errors.New("invalid AGENTCTL_AGENT_CMD")
	ErrGitMissing      = errors.New("git missing")
	ErrTmuxMissing     = errors.New("tmux missing")
	ErrAgentMissing    = errors.New("agent command missing")
	ErrInvalidAgent    = errors.New("unknown agent")
	// ErrCodexNotInteractive: agentctl codex was given a non-interactive
	// Codex subcommand (exec, review, login, …); only the TUI has a composer.
	ErrCodexNotInteractive = errors.New("codex subcommand is not interactive")
	// ErrNotInPane: agentctl codex can't prove it runs inside the tmux pane
	// it would register ($TMUX/$TMUX_PANE inherited, or another server).
	ErrNotInPane      = errors.New("not running inside the pane")
	ErrNotARepo       = errors.New("not a git repository")
	ErrNoCommits      = errors.New("repository has no commits")
	ErrTaskExists     = errors.New("task exists")
	ErrTaskNotFound   = errors.New("task not found")
	ErrBranchExists   = errors.New("branch exists")
	ErrWorktreeExists = errors.New("worktree path exists")
	ErrSessionExists  = errors.New("tmux session exists")
	ErrWorktreeFailed = errors.New("worktree creation failed")
	ErrLaunchFailed   = errors.New("agent launch failed")
)

// Error is a user-facing failure: Kind for errors.Is, Msg for display, Err
// for the underlying cause.
type Error struct {
	Kind error
	Msg  string
	Err  error
}

func (e *Error) Error() string { return e.Msg }

// Unwrap exposes both the kind and the cause.
func (e *Error) Unwrap() []error {
	if e.Err != nil {
		return []error{e.Kind, e.Err}
	}
	return []error{e.Kind}
}

// userErr builds an *Error whose message is forced onto one line.
func userErr(kind, cause error, format string, args ...any) *Error {
	return &Error{Kind: kind, Msg: OneLine(fmt.Sprintf(format, args...)), Err: cause}
}

// OneLine collapses whitespace runs (including newlines) into single spaces so
// every error the user sees fits on one line.
func OneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
