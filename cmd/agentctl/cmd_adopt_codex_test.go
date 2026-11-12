package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
)

// A normally started Codex pane gets a note pointing to agentctl codex, on
// stderr, in the listing and when adopted.
func TestAdoptCodexPaneHint(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	b, err := os.ReadFile(testenv.BuildFakeAgent(t))
	if err != nil {
		t.Fatal(err)
	}
	cx := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cx, b, 0o755); err != nil {
		t.Fatal(err)
	}
	pane := strings.TrimSpace(f.srv.Run(t, "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "cx", "-c", f.repo.Root, cx, "--settings", ""))
	testenv.WaitFor(t, 0, "pane runs codex", func() (bool, string) {
		out, _ := f.srv.TryRun("display-message", "-p", "-t", pane, "#{pane_current_command}")
		return out == "codex", out
	})
	want := "pane " + pane + " runs codex; agentctl can't track it. Restart it with: agentctl codex"
	r := runCmd(t, f.env, f.repo.Root, "adopt")
	if r.code != 0 || !strings.Contains(r.stderr, "agentctl: note: "+want) {
		t.Fatalf("adopt listing = %+v", r)
	}
	r = runCmd(t, f.env, f.repo.Root, "adopt", pane, "--name", "cx")
	if r.code != 0 || !strings.Contains(r.stderr, "agentctl: warning: "+want) || !strings.HasPrefix(r.stdout, "adopted "+pane) {
		t.Fatalf("adopt codex pane = %+v", r)
	}
}
