package main

import (
	"context"
	"strings"

	"github.com/jffypak/agentctl/internal/hookingest"
)

// cmdHook: agentctl hook — the Claude Code hook entry point.
//
// Claude Code spawns it once per hook event with the payload JSON on stdin.
// It always exits 0 and never writes to stdout (Claude Code feeds
// SessionStart / UserPromptSubmit hook stdout into the model's context, so
// any output would be a prompt injection), finishes within the hook budget,
// and appends errors to <home>/logs/hook-errors.log (internal/hookingest).
// The only argument read is `--agent <name>` / `--agent=<name>` (claude or
// codex; default claude), parsed by hand so no usage text can ever reach
// stdout. Anything else is ignored.
func cmdHook(ctx context.Context, sys *system, args []string) int {
	hookingest.Run(ctx, hookingest.Config{
		Agent:      hookAgentArg(args),
		Getenv:     sys.getenv,
		Stdin:      sys.stdin,
		Stderr:     sys.stderr,
		Executable: sys.executable,
		Now:        sys.now,
	})
	return exitOK
}

// hookAgentArg returns the first --agent value in args ("" when absent).
func hookAgentArg(args []string) string {
	for i, a := range args {
		switch {
		case a == "--agent" && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(a, "--agent="):
			return strings.TrimPrefix(a, "--agent=")
		}
	}
	return ""
}
