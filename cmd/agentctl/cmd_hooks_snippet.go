package main

import (
	"context"
	"fmt"

	"github.com/jffypak/agentctl/internal/hookingest"
)

// cmdHooksSnippet: agentctl hooks-snippet
//
// Prints (stdout, JSON only) exactly the hook settings `agentctl new` writes
// to <home>/hooks/claude-settings.json, without that file's status line (so
// merging it never replaces your own). Merging its "hooks" object into your
// own Claude settings is how an adopted pane gets hook-driven state; the hook
// then matches the pane by identity (no AGENTCTL_TASK_ID needed).
func cmdHooksSnippet(_ context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "hooks-snippet")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 0 {
		return sys.usageError("hooks-snippet", "usage: agentctl hooks-snippet (takes no arguments)")
	}
	b, err := hookingest.SettingsSnippet(sys.executable)
	if err != nil {
		return sys.fail(err)
	}
	if _, err := sys.stdout.Write(b); err != nil {
		return sys.fail(err)
	}
	fmt.Fprintln(sys.stderr, `agentctl hooks-snippet: merge the "hooks" object into ~/.claude/settings.json (or a project's .claude/settings.json) so adopted panes report state`)
	return exitOK
}
