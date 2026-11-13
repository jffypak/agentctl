package main

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"

	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/quota"
)

// statusLineInputMax bounds the status line input read from Claude Code.
const statusLineInputMax = 1 << 20

// statusLineUserTimeout bounds the user's own status line command.
const statusLineUserTimeout = 5 * time.Second

// cmdStatusLine: agentctl statusline — the status line of every Claude Code
// session agentctl launches (the launch settings name it).
//
// Claude Code reports the account's rate limits (5-hour and weekly quota)
// only on a status line command's stdin, so this saves them to
// <home>/quota/claude.json for the TUI's quota panel. It then runs the
// status line the user configured in their own settings (which agentctl's
// --settings otherwise hides) with the same input and prints its output.
// Without one it prints nothing. It always exits 0: a status line error
// must never disturb the agent.
func cmdStatusLine(ctx context.Context, sys *system, _ []string) int {
	input, err := io.ReadAll(io.LimitReader(sys.stdin, statusLineInputMax))
	if err != nil {
		return exitOK
	}
	if snap, ok := quota.ParseClaudeStatus(input, sys.now()); ok {
		if p, err := paths.Resolve(sys.getenv); err == nil {
			_ = quota.SaveClaude(p.Home, snap) // best effort: the panel shows the previous snapshot
		}
	}
	if sys.getenv(claude.EnvStatusLineNested) != "" {
		return exitOK
	}
	user := claude.UserStatusLine(input, sys.getenv)
	if user == "" {
		return exitOK
	}
	ctx, cancel := context.WithTimeout(ctx, statusLineUserTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", user)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = sys.stdout
	cmd.Env = append(append([]string(nil), sys.environ...), claude.EnvStatusLineNested+"=1")
	if sys.cwd != "" {
		cmd.Dir = sys.cwd
	}
	_ = cmd.Run()
	return exitOK
}
