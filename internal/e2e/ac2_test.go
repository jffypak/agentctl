package e2e

import (
	"fmt"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
)

// TestAC2_CloseTUIAgentsSurviveReopenRecovers — spec §11: "Close the TUI;
// agents continue running; reopen and recover mappings."
//
// The TUI is closed twice, once with q and once with SIGHUP (what tmux sends
// when a popup closes). Each time both agents keep the very same process
// (pane id, pane pid and server pid unchanged). A fresh TUI process then
// shows both tasks with the identity recorded at launch, and a prompt
// composed in it reaches the right agent and is acknowledged.
func TestAC2_CloseTUIAgentsSurviveReopenRecovers(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=200")
	r.newAgent("alpha", "")
	r.newAgent("beta", "")
	before := map[string]lsTask{"alpha": r.waitState("alpha", "ready"), "beta": r.waitState("beta", "ready")}

	for i, closeWith := range []string{"q", "SIGHUP"} {
		ui := r.startTUI(fmt.Sprintf("ui-%d", i), nil)
		ui.WaitText(t, "alpha", "beta", "ready")
		if closeWith == "SIGHUP" {
			ui.Hangup(t)
		} else {
			ui.Keys(t, closeWith)
		}
		if st := ui.WaitExit(t); st != "0" {
			t.Fatalf("close with %s: exit status %s\n%s", closeWith, st, ui.History())
		}
		for name, tk := range before {
			if !r.paneAlive(tk.identity()) {
				t.Fatalf("after closing the TUI with %s, %s's agent is gone", closeWith, name)
			}
		}
	}

	// reopen: a new process recovers every mapping from the registry
	ui := r.startTUI("ui-reopen", nil)
	ui.WaitText(t, "alpha", "beta", "ready")
	after := r.tasks()
	for name, tk := range before {
		if got := after[name]; got.identity() != tk.identity() || got.Worktree != tk.Worktree || got.Branch != tk.Branch {
			t.Fatalf("%s's mapping changed across TUI restarts:\nbefore %+v\nafter  %+v", name, tk, got)
		}
	}
	a := before["alpha"]
	ui.Select(t, "alpha")
	ui.Keys(t, "Enter")
	ui.WaitText(t, "alpha  IDLE · ready",
		fmt.Sprintf("jpark-alpha · %s %s · pane pid %d", a.Session.SessionID, a.Session.PaneID, a.Session.PanePID),
		"output     alive")

	// the recovered mapping is live: a prompt from the reopened TUI lands in alpha
	ui.Keys(t, "p")
	ui.WaitText(t, "prompt  alpha", "ready · delivered when you send")
	ui.Type(t, "after reopen")
	ui.Keys(t, "C-s")
	ui.WaitText(t, "acknowledged (hash) · after reopen")
	if m := r.waitMessage("alpha", domain.MsgAcknowledged); m.AckMatch != domain.AckHash {
		t.Fatalf("message = %+v", m)
	}
	if p := r.pastes("alpha"); len(p) != 1 || p[0].Text != "after reopen" || p[0].State != "ready" {
		t.Fatalf("alpha received %+v", p)
	}
	if in := r.inputs("beta"); len(in) != 0 {
		t.Fatalf("beta received alpha's prompt: %+v", in)
	}
	ui.Keys(t, "Escape") // detail -> list (a lone ESC: never batch it with the next key, or it reads as alt+key)
	ui.WaitText(t, "↵ open")
	ui.Keys(t, "q")
	if st := ui.WaitExit(t); st != "0" {
		t.Fatalf("exit status %s", st)
	}
	if o, c := r.eventCount(domain.EventUIOpened), r.eventCount(domain.EventUIClosed); o != 3 || c != 3 {
		t.Fatalf("ui.opened %d ui.closed %d, want 3 each", o, c)
	}
}
