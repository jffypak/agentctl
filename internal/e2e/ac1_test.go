package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
)

// TestAC1_TwoAgentsSwitchFromTUI — spec §11: "Start two agents in separate
// worktrees and switch between them from the TUI."
//
// Both agents are started from the TUI's n form (real terminal, real keys).
// Each lands in its own worktree and branch. Then, as from a popup, the TUI
// selects one agent with j/k, opens its detail and attaches. A tmux
// control-mode client (a real attached client) confirms that every switch
// moved it to the chosen agent's session and that the TUI exited cleanly.
func TestAC1_TwoAgentsSwitchFromTUI(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=200")
	r.srv.Run(t, "new-session", "-d", "-s", "home", "--", "sleep", "3600")
	client := r.srv.AttachControlClient(t, "=home")

	ui := r.startTUI("ui-new", r.agentEnv("fleet"))
	ui.WaitText(t, "No agents yet")
	for _, name := range []string{"alpha", "beta"} {
		ui.Keys(t, "n")
		ui.WaitText(t, "new task")
		ui.Type(t, name)
		ui.Keys(t, "Enter")
		ui.Type(t, "write the "+name+" notes")
		ui.Keys(t, "Enter")
		ui.WaitText(t, "started "+name)
	}
	ui.Keys(t, "q")
	if st := ui.WaitExit(t); st != "0" {
		t.Fatalf("TUI exit status %s", st)
	}

	// separate worktrees and branches, each with its own live agent
	alpha, beta := r.waitState("alpha", "ready"), r.waitState("beta", "ready")
	if alpha.Worktree == beta.Worktree || alpha.Session.SessionID == beta.Session.SessionID {
		t.Fatalf("agents share a worktree or session: %+v %+v", alpha, beta)
	}
	for _, tk := range []lsTask{alpha, beta} {
		if tk.Branch != "jpark/"+tk.ID || tk.Worktree != filepath.Join(r.repo.WorktreesDir(), tk.ID) {
			t.Fatalf("%s: branch %s worktree %s", tk.ID, tk.Branch, tk.Worktree)
		}
		if _, err := os.Stat(filepath.Join(tk.Worktree, "README.md")); err != nil {
			t.Fatalf("%s worktree not checked out: %v", tk.ID, err)
		}
		if got := testenv.GitIn(t, r.env, tk.Worktree, "rev-parse", "--abbrev-ref", "HEAD"); got != tk.Branch {
			t.Fatalf("%s worktree is on %s", tk.ID, got)
		}
	}
	if got := client.Session(t); got != "home" {
		t.Fatalf("creating agents moved the client to %q", got)
	}

	// switch: alpha, then beta, then back to alpha — each from a fresh TUI,
	// as a popup is opened afresh every time
	for i, name := range []string{"alpha", "beta", "alpha"} {
		ui := r.startTUI("ui-"+string(rune('a'+i)), nil)
		ui.WaitText(t, "alpha", "beta")
		ui.Select(t, name)
		ui.Keys(t, "Enter")
		ui.WaitText(t, name+"  IDLE · ready", "↵ attach")
		ui.Keys(t, "Enter")
		if st := ui.WaitExit(t); st != "0" {
			t.Fatalf("switch %d: TUI exit status %s\n%s", i, st, ui.History())
		}
		want := "agentctl-" + name
		testenv.WaitFor(t, stateWait, "the client on "+want, func() (bool, string) {
			got := client.Session(t)
			return got == want, got
		})
		pane := r.srv.Run(t, "list-clients", "-F", "#{pane_id}")
		if tk := r.task(name); pane != tk.Session.PaneID {
			t.Fatalf("switch %d: client on pane %s, %s's pane is %s", i, pane, name, tk.Session.PaneID)
		}
	}

	// both agents are untouched by the switching, and each attach was counted
	for _, tk := range []lsTask{alpha, beta} {
		if !r.paneAlive(tk.identity()) {
			t.Fatalf("%s's agent is gone after switching", tk.ID)
		}
	}
	out, errOut, code := r.agentctl("stats", "--json")
	var stats struct {
		Attaches int    `json:"attaches"`
		Median   *int64 `json:"median_open_to_first_action_ms"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &stats) != nil {
		t.Fatalf("stats: %d %s\n%s", code, errOut, out)
	}
	if stats.Attaches != 3 || stats.Median == nil {
		t.Fatalf("stats = %s", out)
	}
}
