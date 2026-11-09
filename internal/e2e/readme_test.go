package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestREADMEQuickstartAndTroubleshooting keeps the README honest: the
// quickstart walks install -> binding -> new -> popup -> send -> diff ->
// close in that order, and the troubleshooting table has a line for each
// ARCHITECTURE.md error state, quoting the message the user actually sees
// (internal/controller's one-line errors).
func TestREADMEQuickstartAndTroubleshooting(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join(filepath.Dir(demoScript(t)), "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	quick, trouble, ok := strings.Cut(readme, "## Troubleshooting")
	if !ok {
		t.Fatal("no Troubleshooting section")
	}
	at := 0
	for _, step := range []string{
		"**1. Install**", "go install github.com/jffypak/agentctl/cmd/agentctl",
		"**2. Add the tmux binding.", "agentctl tmux-binding",
		"**3. Start an agent**", "agentctl new ",
		"**4. Open the popup**", "prefix a",
		"**5. Send a prompt.**", "agentctl send ",
		"**6. Review the diff.**", "agentctl diff ",
		"**7. Close the task.**", "agentctl close ",
		"agentctl stats",
	} {
		i := strings.Index(quick[at:], step)
		if i < 0 {
			t.Fatalf("quickstart: %q missing or out of order", step)
		}
		at += i + len(step)
	}
	for state, msg := range map[string]string{
		"tmux missing":     "tmux not found on PATH",
		"git missing":      "git not found on PATH",
		"not a git repo":   "is not inside a git repository",
		"claude missing":   "agent command \"claude\" not found on PATH",
		"branch exists":    "branch agentctl/<name> already exists",
		"worktree exists":  "worktree path … already exists",
		"session exists":   "tmux session agentctl-<name> already exists",
		"no commits":       "has no commits yet",
		"registry":         "cannot open registry",
		"silent launch":    "silent since launch · attach to check",
		"unknown adopted":  "queued · attach to deliver",
		"disconnected":     "disconnected · pane_missing",
		"unconfirmed":      "message unconfirmed · attach to check",
		"stale":            "state stale — attach to deliver",
		"attach refused":   "attach refused",
		"stop refused":     "refusing to stop",
		"hooks fail open":  "hook-errors.log",
		"no destructive":   "never deletes",
		"agents outlive":   "Quitting never stops an agent",
		"hermetic testing": "Tests never touch your tmux server",
	} {
		if !strings.Contains(readme, msg) {
			t.Errorf("README has no line for %s (%q)", state, msg)
		}
	}
	if n := strings.Count(trouble, "\n| `") + strings.Count(trouble, "\n| Agent") + strings.Count(trouble, "\n| Hook"); n < 15 {
		t.Errorf("troubleshooting table has %d rows", n)
	}
}
