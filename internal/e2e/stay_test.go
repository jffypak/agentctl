package e2e

import (
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
)

// TestStayDashboard: `agentctl tui --stay` in its own pane is a dashboard.
// An attach moves the user's client to the agent, as from a popup, but the
// TUI keeps running, so the same process can switch to a second agent and
// quits only on q.
func TestStayDashboard(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=200")
	r.newAgent("alpha", "")
	r.newAgent("beta", "")
	r.waitState("alpha", "ready")
	r.waitState("beta", "ready")
	r.srv.Run(t, "new-session", "-d", "-s", "home", "--", "sleep", "3600")
	client := r.srv.AttachControlClient(t, "=home")

	ui := r.srv.StartTUI(t, r.bin, r.env, testenv.TUIOptions{Session: "dash", Cwd: r.repo.Root, Args: []string{"tui", "--stay"}})
	ui.WaitText(t, "agents / repo", "alpha", "beta")
	for i, name := range []string{"alpha", "beta"} {
		if i > 0 {
			ui.Keys(t, "Escape") // detail -> list (a lone ESC)
			ui.WaitText(t, "↵ open")
		}
		ui.Select(t, name)
		ui.Keys(t, "Enter")
		ui.WaitText(t, name+"  IDLE · ready", "↵ attach")
		ui.Keys(t, "Enter")
		want := "agentctl-" + name
		testenv.WaitFor(t, stateWait, "the client on "+want, func() (bool, string) {
			got := client.Session(t)
			return got == want, got
		})
		ui.WaitText(t, "switched to "+name+" · prefix L returns here")
		if !ui.Alive(t) {
			t.Fatalf("switch to %s: the --stay TUI exited\n%s", name, ui.History())
		}
	}
	ui.Keys(t, "Escape") // detail -> list; q in the detail only goes back
	ui.WaitText(t, "↵ open")
	ui.Keys(t, "q")
	if st := ui.WaitExit(t); st != "0" {
		t.Fatalf("exit status %s\n%s", st, ui.History())
	}
}
