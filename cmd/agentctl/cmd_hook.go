package main

import (
	"context"

	"github.com/jffypak/agentctl/internal/hookingest"
)

// cmdHook: agentctl hook — the Claude Code hook entry point.
//
// Claude Code spawns it once per hook event with the payload JSON on stdin.
// It always exits 0 and never writes to stdout (Claude Code feeds
// SessionStart / UserPromptSubmit hook stdout into the model's context, so
// any output would be a prompt injection), finishes within the hook budget,
// and appends errors to <home>/logs/hook-errors.log (internal/hookingest).
// Arguments are ignored: no usage text can ever reach stdout.
func cmdHook(ctx context.Context, sys *system, _ []string) int {
	hookingest.Run(ctx, hookingest.Config{
		Getenv:     sys.getenv,
		Stdin:      sys.stdin,
		Stderr:     sys.stderr,
		Executable: sys.executable,
		Now:        sys.now,
	})
	return exitOK
}
