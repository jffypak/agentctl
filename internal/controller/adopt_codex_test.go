package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
)

// codexNamedAgent is a copy of the test-built fakeagent named "codex", so
// tmux reports the pane command as codex (macOS reports a symlink by its
// target's name and refuses to run a copied system binary).
func codexNamedAgent(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(testenv.BuildFakeAgent(t))
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A normally started Codex can't be tracked (its hooks run in the shared
// daemon): the listing says so and points to agentctl codex.
func TestAdoptCandidatesCodexHint(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	ctx := context.Background()
	cx := f.userPane(t, "cx", f.repo.Root, codexNamedAgent(t), "--settings", "")
	testenv.WaitFor(t, 0, "pane runs codex", func() (bool, string) {
		out, _ := f.srv.TryRun("display-message", "-p", "-t", cx.PaneID, "#{pane_current_command}")
		return out == "codex", out
	})
	list, err := f.ctl.ListAdoptCandidates(ctx, CandidateQuery{})
	if err != nil {
		t.Fatal(err)
	}
	want := "pane " + cx.PaneID + " runs codex; agentctl can't track it. Restart it with: agentctl codex"
	if len(list.Hints) != 1 || list.Hints[0] != want {
		t.Fatalf("hints = %q, want [%q]", list.Hints, want)
	}
	res, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: cx.PaneID, Name: "cx"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Warning, "agentctl codex") {
		t.Fatalf("adopt warning = %q", res.Warning)
	}
	if list, _ := f.ctl.ListAdoptCandidates(ctx, CandidateQuery{}); len(list.Hints) != 0 {
		t.Fatalf("hint for an adopted pane: %q", list.Hints)
	}
}
