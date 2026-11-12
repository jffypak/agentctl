package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// TestInv6_FailedStoppedAndUnknownNeverRenderAsSuccess — invariant 6
// ("Unknown is honest") on a real terminal: an agent that exits 3 is failed
// in NEEDS YOU, one that exits 0 is "exited" (never "ready" or done), and an
// adopted pane without hooks has sent no event, so it is in the muted
// INITIALSTATE section with "no events yet" (state unknown). None of
// these rows uses the words of success.
func TestInv6_FailedStoppedAndUnknownNeverRenderAsSuccess(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=200")
	r.newAgent("crasher", "#exit 3")
	r.newAgent("quitter", "#exit 0")
	r.waitState("crasher", "failed")
	r.waitState("quitter", "stopped")

	empty := filepath.Join(r.logs, "no-hooks-settings.json")
	writeFile(t, empty, "{}")
	created, err := r.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: "by-hand", Dir: r.repo.Root,
		Argv: []string{"/usr/bin/env", "FAKEAGENT_LOG=" + r.inputLog("loner"), r.fake, "--settings", empty},
	})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, stateWait, "the hand-started agent's prompt", func() (bool, string) {
		out, _ := r.srv.TryRun("capture-pane", "-p", "-t", created.PaneID)
		return strings.HasSuffix(strings.TrimSpace(out), ">"), out
	})
	if out, errOut, code := r.agentctl("adopt", created.PaneID, "--name", "loner"); code != 0 {
		t.Fatalf("adopt: %d %s %s", code, out, errOut)
	}
	r.keysToPane(created.PaneID, "a turn nobody reports", true) // it works and finishes, but no hook says so

	ui := r.startTUI("ui", nil)
	s := ui.WaitScreen(t, "failed, exited and unknown rows", func(s string) bool {
		return sectionOf(s, "crasher") == "NEEDS YOU" && strings.Contains(rowOf(s, "crasher"), "failed · exit 3") &&
			sectionOf(s, "quitter") == "IDLE" && strings.Contains(rowOf(s, "quitter"), "exited · exit 0") &&
			sectionOf(s, "loner") == "INITIALSTATE" && strings.Contains(rowOf(s, "loner"), "no events yet")
	})
	for _, name := range []string{"crasher", "quitter", "loner"} {
		row := strings.ToLower(rowOf(s, name))
		for _, success := range []string{"ready", "done", "success", "finished", "complete"} {
			if strings.Contains(row, success) {
				t.Fatalf("%s renders as %q: %s", name, success, row)
			}
		}
	}
	if tk := r.task("loner"); tk.AgentState != "unknown" || tk.Confidence != "none" {
		t.Fatalf("loner = %+v", tk)
	}
	quit(t, ui)
}

// TestInv9_CloseNeverDeletesWorktreeOrBranch — invariant 9 ("No destructive
// git") and invariant 2 ("only an explicit, confirmed close-with-stop" ends
// an agent), end to end. alpha is closed from the TUI (x, y): it disappears
// from the list, its agent keeps running. beta is closed with `close --stop
// --yes`: its tmux session ends. Both worktrees (with their uncommitted
// edits) and both branches are still there.
func TestInv9_CloseNeverDeletesWorktreeOrBranch(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=200")
	r.newAgent("alpha", "")
	r.newAgent("beta", "")
	alpha, beta := r.waitState("alpha", "ready"), r.waitState("beta", "ready")
	for _, tk := range []lsTask{alpha, beta} {
		writeFile(t, filepath.Join(tk.Worktree, "wip.txt"), "uncommitted work of "+tk.ID+"\n")
	}

	ui := r.startTUI("ui", nil)
	ui.Select(t, "alpha")
	ui.Keys(t, "x")
	ui.WaitText(t, "close alpha? y/N", "keeps worktree", "keeps branch agentctl/alpha")
	ui.Keys(t, "y")
	ui.WaitScreen(t, "alpha gone from the list", func(s string) bool {
		return rowOf(s, "alpha") == "" && rowOf(s, "beta") != ""
	})
	quit(t, ui)
	if tk := r.task("alpha"); tk.TaskState != "closed" || !r.paneAlive(alpha.identity()) {
		t.Fatalf("after a plain close: %+v, agent alive %v", tk, r.paneAlive(alpha.identity()))
	}

	if out, errOut, code := r.agentctl("close", "beta", "--stop", "--yes"); code != 0 {
		t.Fatalf("close --stop: %d %s %s", code, out, errOut)
	}
	if _, err := r.srv.TryRun("has-session", "-t", "="+beta.Session.Name); err == nil {
		t.Fatal("close --stop left beta's session running")
	}

	branches := r.repo.Git(t, "branch", "--list", "agentctl/*")
	worktrees := r.repo.Git(t, "worktree", "list", "--porcelain")
	for _, tk := range []lsTask{alpha, beta} {
		b, err := os.ReadFile(filepath.Join(tk.Worktree, "wip.txt"))
		if err != nil || string(b) != "uncommitted work of "+tk.ID+"\n" {
			t.Fatalf("%s's uncommitted work: %q %v", tk.ID, b, err)
		}
		if !strings.Contains(branches, tk.Branch) || !strings.Contains(worktrees, "worktree "+tk.Worktree+"\n") {
			t.Fatalf("%s's branch or worktree is gone:\n%s\n%s", tk.ID, branches, worktrees)
		}
	}
}
