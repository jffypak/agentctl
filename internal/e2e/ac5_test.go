package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// TestAC5_StalePaneShownDisconnected — spec §11: "A stale/missing pane is
// detected and shown as disconnected rather than misattributed to another
// agent."
//
//   - killed: alpha's pane disappears while the TUI is open. The next
//     reconcile tick moves alpha to NEEDS YOU as disconnected; beta is
//     untouched; attach is refused and the user's client stays put; a send
//     is rejected.
//   - reused: the tmux server restarts and a different program (the
//     "intruder") gets alpha's old pane id %N, and even claims to be alpha in
//     its hook env. alpha shows disconnected; its detail, logs and `show`
//     never display the intruder's screen; attach is refused; a send is
//     rejected and nothing reaches the intruder; its hooks are orphans that
//     change nothing.
func TestAC5_StalePaneShownDisconnected(t *testing.T) {
	t.Parallel()
	t.Run("killed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "FAKEAGENT_WORK_MS=200")
		r.newAgent("alpha", "")
		r.newAgent("beta", "")
		alpha, beta := r.waitState("alpha", "ready"), r.waitState("beta", "ready")
		r.srv.Run(t, "new-session", "-d", "-s", "home", "--", "sleep", "3600")
		client := r.srv.AttachControlClient(t, "=home")

		ui := r.startTUI("ui", nil)
		ui.WaitText(t, "alpha", "beta", "IDLE")
		if err := r.srv.Client().KillVerifiedSession(context.Background(), alpha.identity()); err != nil {
			t.Fatal(err)
		}
		// detected by the TUI's own reconcile tick: no CLI call in between
		ui.WaitScreen(t, "alpha disconnected in NEEDS YOU, beta still ready", func(s string) bool {
			return sectionOf(s, "alpha") == "NEEDS YOU" && strings.Contains(rowOf(s, "alpha"), "disconnected · pane_missing") &&
				sectionOf(s, "beta") == "IDLE" && strings.Contains(rowOf(s, "beta"), "ready")
		})
		assertAttachRefused(t, ui, client, "alpha", "home")
		if !r.paneAlive(beta.identity()) {
			t.Fatal("beta's agent is gone")
		}
		if tk := r.task("beta"); tk.AgentState != "ready" || tk.identity() != beta.identity() {
			t.Fatalf("beta = %+v", tk)
		}
		assertSendRejected(t, r, "alpha")
		quit(t, ui)
	})

	t.Run("reused", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, "FAKEAGENT_WORK_MS=200")
		r.newAgent("alpha", "")
		r.newAgent("beta", "")
		alpha, beta := r.waitState("alpha", "ready"), r.waitState("beta", "ready")

		r.srv.Restart(t) // a crash or the user's kill-server: pane ids start over
		intruderLog := r.inputLog("intruder")
		created, err := r.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
			Name: "intruder", Dir: r.repo.Root,
			Argv: []string{"/usr/bin/env", "AGENTCTL_TASK_ID=alpha", "AGENTCTL_HOME=" + r.paths.Home,
				"FAKEAGENT_LOG=" + intruderLog, "FAKEAGENT_HOOK_LOG=" + filepath.Join(r.logs, "intruder.hooks.jsonl"),
				r.fake, "--settings", r.paths.ClaudeSettings},
		})
		if err != nil {
			t.Fatal(err)
		}
		if created.PaneID != alpha.Session.PaneID {
			t.Fatalf("the intruder got pane %s, alpha had %s: no reuse to test", created.PaneID, alpha.Session.PaneID)
		}
		testenv.WaitFor(t, stateWait, "the intruder's prompt", func() (bool, string) {
			out, _ := r.srv.TryRun("capture-pane", "-p", "-t", created.PaneID)
			return strings.HasSuffix(strings.TrimSpace(out), ">"), out
		})
		r.keysToPane(created.PaneID, "say INTRUDER-SECRET", true)
		testenv.WaitFor(t, stateWait, "the intruder's turn and its orphan hooks", func() (bool, string) {
			out, _ := r.srv.TryRun("capture-pane", "-p", "-t", created.PaneID)
			_, after, ok := strings.Cut(out, "INTRUDER-SECRET")
			return ok && strings.Contains(after, "done") && r.eventCount(domain.EventHookOrphan) >= 4, out
		})
		r.srv.Run(t, "new-session", "-d", "-s", "home", "--", "sleep", "3600")
		client := r.srv.AttachControlClient(t, "=home")

		ui := r.startTUI("ui", nil)
		s := ui.WaitScreen(t, "alpha and beta disconnected in NEEDS YOU", func(s string) bool {
			return sectionOf(s, "alpha") == "NEEDS YOU" && strings.Contains(rowOf(s, "alpha"), "disconnected") &&
				sectionOf(s, "beta") == "NEEDS YOU" && strings.Contains(rowOf(s, "beta"), "disconnected")
		})
		assertNoSecret(t, "list", s)
		ui.Select(t, "alpha")
		ui.Keys(t, "Enter")
		s = ui.WaitText(t, "alpha  NEEDS YOU · disconnected", "output     pane unavailable (disconnected")
		assertNoSecret(t, "detail", s)
		ui.Keys(t, "l")
		s = ui.WaitText(t, "logs  alpha", "pane unavailable")
		assertNoSecret(t, "logs", s)
		ui.Keys(t, "Escape")
		ui.WaitText(t, "alpha  NEEDS YOU", "↵ attach")
		ui.Keys(t, "Escape")
		ui.WaitText(t, "↵ open")
		assertAttachRefused(t, ui, client, "alpha", "home")
		out, errOut, _ := r.agentctl("show", "alpha")
		assertNoSecret(t, "agentctl show", out+errOut)
		if !strings.Contains(out, "pane unavailable (disconnected") {
			t.Fatalf("show alpha:\n%s", out)
		}
		assertSendRejected(t, r, "alpha")
		for _, in := range r.inputs("intruder") {
			if in.Kind == "paste" || in.Text != "say INTRUDER-SECRET" && in.Kind == "submit" {
				t.Fatalf("something reached the intruder: %+v", in)
			}
		}
		// the intruder's hooks claimed alpha and changed nothing
		for name, before := range map[string]lsTask{"alpha": alpha, "beta": beta} {
			tk := r.task(name)
			if tk.AgentState != "disconnected" || tk.identity() != before.identity() {
				t.Fatalf("%s re-attributed: %+v", name, tk)
			}
		}
		for _, e := range r.stateChanges("alpha") {
			if e != "starting>ready" && !strings.HasSuffix(e, ">disconnected") {
				t.Fatalf("alpha's state history %v: a hook from the reused pane moved it", r.stateChanges("alpha"))
			}
		}
		quit(t, ui)
	})
}

func assertNoSecret(t *testing.T, where, s string) {
	t.Helper()
	if strings.Contains(s, "INTRUDER") {
		t.Fatalf("%s shows the reused pane's output:\n%s", where, s)
	}
}

// assertAttachRefused: enter on name's detail refuses with a flash, the TUI
// stays, and the user's client is still on session.
func assertAttachRefused(t *testing.T, ui *testenv.TUI, client *testenv.ControlClient, name, session string) {
	t.Helper()
	ui.Select(t, name)
	ui.Keys(t, "Enter")
	ui.WaitText(t, name+"  NEEDS YOU · disconnected", "↵ attach")
	ui.Keys(t, "Enter")
	ui.WaitText(t, "attach refused: "+name)
	if !ui.Alive(t) {
		t.Fatal("the TUI exited on a refused attach")
	}
	if got := client.Session(t); got != session {
		t.Fatalf("a refused attach moved the client to %q", got)
	}
	ui.Keys(t, "Escape")
	ui.WaitText(t, "↵ open")
}

func assertSendRejected(t *testing.T, r *rig, name string) {
	t.Helper()
	if _, errOut, code := r.agentctl("send", name, "hello?"); code != 1 || !strings.Contains(errOut, "send rejected") {
		t.Fatalf("send to disconnected %s: exit %d %q", name, code, errOut)
	}
	if ms := r.messages(name); len(ms) != 0 {
		t.Fatalf("a rejected send stored %s", describeMessages(ms))
	}
}

func quit(t *testing.T, ui *testenv.TUI) {
	t.Helper()
	ui.Keys(t, "q")
	if st := ui.WaitExit(t); st != "0" {
		t.Fatalf("exit status %s", st)
	}
}

// rowOf is the list row of task name ("" if absent).
func rowOf(screen, name string) string {
	for _, l := range strings.Split(screen, "\n") {
		if f := strings.Fields(strings.TrimPrefix(strings.TrimPrefix(l, " > "), "   ")); len(f) > 0 && f[0] == name &&
			(strings.HasPrefix(l, " > ") || strings.HasPrefix(l, "   ")) {
			return l
		}
	}
	return ""
}

// sectionOf is the title of the list section holding task name.
func sectionOf(screen, name string) string {
	section := ""
	for _, l := range strings.Split(screen, "\n") {
		switch strings.TrimSpace(l) {
		case "NEEDS YOU", "IDLE", "RUNNING", "UNKNOWN", "CLOSED":
			section = strings.TrimSpace(l)
			continue
		}
		if rowOf(l, name) != "" {
			return section
		}
	}
	return ""
}
