package e2e

import (
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
)

// TestCommandCenterUnreadAndDetails: a finished turn makes the task unread
// (ls --json and the Unread tab), the wide list shows its reply and prompt
// in the details pane, and opening the task clears the mark.
func TestCommandCenterUnreadAndDetails(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=100")
	r.newAgent("alpha", "write the changelog")
	tk := r.waitState("alpha", "ready")
	testenv.WaitFor(t, stateWait, "alpha unread", func() (bool, string) {
		tk = r.task("alpha")
		return tk.Unread, tk.Label
	})
	if tk.Model != "fakeagent" {
		t.Fatalf("model = %q", tk.Model)
	}

	ui := r.srv.StartTUI(t, r.bin, r.env, testenv.TUIOptions{Cwd: r.repo.Root, Width: 140, Height: 40})
	ui.WaitText(t, "Unread 1", "● alpha", "Last message", "done", "Prompt", "write the changelog")
	ui.Keys(t, "Enter")
	ui.WaitText(t, "alpha  IDLE · ready", "reply      done") // opening it marked it read
	ui.Keys(t, "Escape")
	ui.WaitText(t, "Unread 0")
	if r.task("alpha").Unread {
		t.Fatal("alpha still unread after opening it")
	}
	ui.Keys(t, "q")
	if st := ui.WaitExit(t); st != "0" {
		t.Fatalf("exit status %s\n%s", st, ui.History())
	}
}
