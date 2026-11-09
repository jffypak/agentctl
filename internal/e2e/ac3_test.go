package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

// TestAC3_RecentOutputAndDiffWithoutLeavingTUI — spec §11: "Select either
// agent, view recent output and diff, and return without searching tmux
// sessions manually."
//
// Each agent edits a tracked file and writes a new one in its own worktree
// and prints a marker line. From one TUI process the user selects each agent
// in turn: the detail shows its diff headline and recent output, l shows its
// output (and never the other agent's), d lists its changed files, and esc
// returns each time. The TUI never leaves, and the user's tmux client never
// moves off the session it was on.
func TestAC3_RecentOutputAndDiffWithoutLeavingTUI(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=200")
	r.srv.Run(t, "new-session", "-d", "-s", "home", "--", "sleep", "3600")
	client := r.srv.AttachControlClient(t, "=home")
	names := []string{"alpha", "beta"}
	for _, name := range names {
		r.newAgent(name, "")
	}
	for _, name := range names {
		tk := r.waitState(name, "ready")
		appendFile(t, filepath.Join(tk.Worktree, "README.md"), "edited by "+name+"\n")
		writeFile(t, filepath.Join(tk.Worktree, name+"-notes.md"), "# "+name+"\nline two\n")
		r.keysToPane(tk.Session.PaneID, "say MARKER-"+name, true)
		testenv.WaitFor(t, stateWait, name+" printed its marker", func() (bool, string) {
			out, _ := r.srv.TryRun("capture-pane", "-p", "-t", tk.Session.PaneID)
			_, after, ok := strings.Cut(out, "MARKER-"+name)
			return ok && strings.Contains(after, "done"), out
		})
	}

	ui := r.startTUI("ui", nil)
	ui.WaitText(t, "alpha", "beta")
	for _, name := range names {
		other := "beta"
		if name == "beta" {
			other = "alpha"
		}
		ui.Select(t, name)
		ui.Keys(t, "Enter")
		// detail: diff headline + recent output, loaded asynchronously
		ui.WaitText(t, name+"  IDLE", "diff       1 file +1 -0 · 1 untracked", "output     alive", "MARKER-"+name)

		ui.Keys(t, "l")
		s := ui.WaitText(t, "logs  "+name+" · alive", "MARKER-"+name, "fakeagent ")
		if strings.Contains(s, "MARKER-"+other) {
			t.Fatalf("%s's logs show %s's output:\n%s", name, other, s)
		}
		ui.Keys(t, "Escape")
		ui.WaitText(t, name+"  IDLE", "↵ attach")

		ui.Keys(t, "d")
		s = ui.WaitText(t, "diff  "+name, "vs main @ ", "README.md", name+"-notes.md", "untracked")
		if strings.Contains(s, other+"-notes.md") {
			t.Fatalf("%s's diff lists %s's file:\n%s", name, other, s)
		}
		ui.Keys(t, "Escape")
		ui.WaitText(t, name+"  IDLE", "↵ attach")
		ui.Keys(t, "Escape")
		ui.WaitText(t, "↵ open")

		if !ui.Alive(t) {
			t.Fatal("the TUI exited")
		}
		if got := client.Session(t); got != "home" {
			t.Fatalf("viewing %s moved the user's client to %q", name, got)
		}
	}
	ui.Keys(t, "q")
	if st := ui.WaitExit(t); st != "0" {
		t.Fatalf("exit status %s", st)
	}
	if n := r.eventCount(domain.EventUIAction); n != 4 { // logs + diff, per agent
		t.Fatalf("%d ui.action events, want 4", n)
	}
	if got := client.Session(t); got != "home" {
		t.Fatalf("the user's client is on %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}
