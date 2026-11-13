package e2e

import (
	"sort"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
)

// panesBySession maps each session to its panes as "<pane id>[:slot=<id>][:placeholder]".
func (r *rig) panesBySession() map[string][]string {
	r.t.Helper()
	out := map[string][]string{}
	// "|" separators: Run trims trailing whitespace, which would eat empty tab fields
	for _, line := range strings.Split(r.srv.Run(r.t, "list-panes", "-a", "-F", "#{session_name}|#{pane_id}|#{@agentctl_dock_slot}|#{@agentctl_placeholder}"), "\n") {
		f := strings.Split(line, "|")
		if len(f) != 4 {
			continue
		}
		p := f[1]
		if f[2] != "" {
			p += ":slot=" + f[2]
		}
		if f[3] == "1" {
			p += ":placeholder"
		}
		out[f[0]] = append(out[f[0]], p)
	}
	for _, ps := range out {
		sort.Strings(ps)
	}
	return out
}

// hasPane reports whether some entry of panes starts with prefix (and ends
// with suffix, when given); a bare pane id must match exactly.
func hasPane(panes []string, prefix string, suffix ...string) bool {
	for _, p := range panes {
		switch {
		case len(suffix) > 0 && strings.HasPrefix(p, prefix) && strings.HasSuffix(p, suffix[0]):
			return true
		case len(suffix) == 0 && (p == prefix || strings.HasSuffix(prefix, "=") && strings.HasPrefix(p, prefix)):
			return true
		}
	}
	return false
}

// TestSplitDashboard: `agentctl tui --split` opens an agent in a pane beside
// the TUI, in the TUI's window, and swaps a placeholder into the agent's
// session so that session lives on. Opening a second agent sends the first
// one home; quitting sends every agent home and removes the placeholder.
// The docked agent keeps its identity: prompts reach it, and close --stop
// still kills only its own session.
func TestSplitDashboard(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=200")
	r.newAgent("alpha", "")
	r.newAgent("beta", "")
	alpha := r.waitState("alpha", "ready").Session.PaneID
	beta := r.waitState("beta", "ready").Session.PaneID

	ui := r.srv.StartTUI(t, r.bin, r.env, testenv.TUIOptions{Session: "dash", Cwd: r.repo.Root, Args: []string{"tui", "--split"}})
	ui.Target = r.srv.Run(t, "display-message", "-p", "-t", ui.Target, "#{pane_id}") // the dock moves focus off the TUI
	host := ui.Target
	ui.WaitText(t, "agents / repo", "alpha", "beta")

	open := func(name string) {
		t.Helper()
		ui.Select(t, name)
		ui.Keys(t, "Enter")
		ui.WaitText(t, name+"  IDLE · ready", "↵ attach")
		ui.Keys(t, "Enter")
		ui.WaitText(t, "opened "+name+" beside the list")
	}

	open("alpha")
	panes := r.panesBySession()
	if got := panes["dash"]; len(got) != 2 || !hasPane(got, host) || !hasPane(got, alpha+":slot=") {
		t.Fatalf("dash panes = %q, want the TUI %s and alpha %s", got, host, alpha)
	}
	if got := panes["jpark-alpha"]; len(got) != 1 || !strings.HasSuffix(got[0], ":placeholder") {
		t.Fatalf("jpark-alpha panes = %q, want one placeholder", got)
	}
	if active := r.srv.Run(t, "display-message", "-p", "-t", "=dash:", "#{pane_id}"); active != alpha {
		t.Fatalf("active pane in dash = %s, want alpha %s", active, alpha)
	}
	// the identity still verifies: a prompt reaches the docked agent
	if _, stderr, code := r.agentctl("send", "alpha", "hello from the dashboard"); code != 0 {
		t.Fatalf("send to docked alpha: exit %d: %s", code, stderr)
	}
	testenv.WaitFor(t, stateWait, "alpha got the prompt", func() (bool, string) {
		for _, in := range r.pastes("alpha") {
			if strings.Contains(in.Text, "hello from the dashboard") {
				return true, ""
			}
		}
		return false, ""
	})

	ui.Keys(t, "Escape")
	ui.WaitText(t, "↵ open")
	open("beta")
	panes = r.panesBySession()
	if got := panes["jpark-alpha"]; len(got) != 1 || got[0] != alpha {
		t.Fatalf("jpark-alpha panes = %q, want alpha %s back home", got, alpha)
	}
	if got := panes["dash"]; len(got) != 2 || !hasPane(got, host) || !hasPane(got, beta+":slot=") {
		t.Fatalf("dash panes = %q, want the TUI and beta %s", got, beta)
	}

	// close --stop on the docked agent moves it home first, then kills only its session
	if _, stderr, code := r.agentctl("close", "beta", "--stop", "--yes"); code != 0 {
		t.Fatalf("close --stop beta: exit %d: %s", code, stderr)
	}
	panes = r.panesBySession()
	if _, ok := panes["jpark-beta"]; ok {
		t.Fatalf("jpark-beta still exists: %v", panes)
	}
	if got := panes["dash"]; len(got) != 2 || !hasPane(got, host) || !hasPane(got, "%", ":placeholder") {
		t.Fatalf("dash panes = %q, want the TUI and the free placeholder", got)
	}
	if !ui.Alive(t) {
		t.Fatalf("the TUI died with beta\n%s", ui.History())
	}

	// a free placeholder is reused for the next dock
	ui.Keys(t, "Escape")
	ui.WaitText(t, "↵ open")
	open("alpha")
	if got := r.panesBySession()["dash"]; len(got) != 2 || !hasPane(got, host) || !hasPane(got, alpha+":slot=") {
		t.Fatalf("dash panes = %q, want the TUI and alpha", got)
	}

	ui.Keys(t, "Escape")
	ui.WaitText(t, "↵ open")
	ui.Keys(t, "q")
	if st := ui.WaitExit(t); st != "0" {
		t.Fatalf("exit status %s\n%s", st, ui.History())
	}
	panes = r.panesBySession()
	if got := panes["jpark-alpha"]; len(got) != 1 || got[0] != alpha {
		t.Fatalf("after quit jpark-alpha panes = %q, want alpha %s (all: %v)\n%s", got, alpha, panes, ui.History())
	}
	if got := panes["dash"]; len(got) != 1 || got[0] != host {
		t.Fatalf("after quit dash panes = %q, want only the TUI %s", got, host)
	}
}
