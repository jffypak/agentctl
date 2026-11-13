package tmux_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// TestDockRoundTripIntegration: a dock swaps the agent pane into the host's
// window and a placeholder into the agent's session; an undock swaps them
// back; the guarded kill removes only a placeholder.
func TestDockRoundTripIntegration(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	ctx := context.Background()
	cl := srv.Client()
	srv.Run(t, "new-session", "-d", "-s", "agent", "--", "sleep", "3600")
	srv.Run(t, "new-session", "-d", "-s", "dash", "--", "sleep", "3600")
	agent := srv.Run(t, "display-message", "-p", "-t", "=agent:", "#{pane_id}")
	host := srv.Run(t, "display-message", "-p", "-t", "=dash:", "#{pane_id}")
	sessionOf := func(pane string) string {
		return srv.Run(t, "display-message", "-p", "-t", pane, "#{session_name}")
	}

	slot, err := cl.SplitPlaceholder(ctx, host, "shown elsewhere; $(touch pwned) #{pane_id}")
	if err != nil {
		t.Fatal(err)
	}
	// the guard refuses a pane that is not a placeholder
	if err := cl.KillPlaceholder(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := cl.KillPlaceholder(ctx, host); err != nil {
		t.Fatal(err)
	}
	if err := cl.Dock(ctx, tmux.DockInCommands(agent, slot)); err != nil {
		t.Fatal(err)
	}
	if sessionOf(agent) != "dash" || sessionOf(slot) != "agent" {
		t.Fatalf("after dock: agent in %s, slot in %s", sessionOf(agent), sessionOf(slot))
	}
	if got, err := cl.DockSlot(ctx, agent); err != nil || got != slot {
		t.Fatalf("DockSlot = %q, %v", got, err)
	}
	panes, err := cl.WindowPanes(ctx, host)
	if err != nil || len(panes) != 2 {
		t.Fatalf("WindowPanes = %+v, %v", panes, err)
	}
	for _, p := range panes {
		if p.PaneID == agent && p.DockSlot != slot || p.PaneID == host && (p.DockSlot != "" || p.Placeholder) {
			t.Fatalf("WindowPanes = %+v", panes)
		}
	}
	if got := srv.Run(t, "display-message", "-p", "-t", "=dash:", "#{pane_id}"); got != agent {
		t.Fatalf("active pane = %s, want the docked agent %s", got, agent)
	}

	if err := cl.Dock(ctx, tmux.UndockCommands(agent, slot)); err != nil {
		t.Fatal(err)
	}
	if sessionOf(agent) != "agent" || sessionOf(slot) != "dash" {
		t.Fatalf("after undock: agent in %s, slot in %s", sessionOf(agent), sessionOf(slot))
	}
	if got, _ := cl.DockSlot(ctx, agent); got != "" {
		t.Fatalf("DockSlot after undock = %q", got)
	}
	if err := cl.KillPlaceholder(ctx, slot); err != nil {
		t.Fatal(err)
	}
	if err := cl.KillPlaceholder(ctx, slot); err != nil { // already gone: not an error
		t.Fatal(err)
	}
	all := srv.Run(t, "list-panes", "-a", "-F", "#{pane_id}")
	if strings.Contains(all, slot) || !strings.Contains(all, agent) || !strings.Contains(all, host) {
		t.Fatalf("panes after kill = %q", all)
	}
}

func TestDockRejectsMalformedPaneIDs(t *testing.T) {
	if _, err := tmux.KillPlaceholderCommand("%1;kill-server"); err == nil {
		t.Fatal("malformed pane id accepted by the kill")
	}
	cmd, err := tmux.KillPlaceholderCommand("%12")
	if err != nil || !strings.HasSuffix(cmd[len(cmd)-1], " -t '%12'") || !strings.Contains(cmd[len(cmd)-2], "#{==:#{pane_id},%12}") ||
		!strings.Contains(cmd[len(cmd)-2], "#{==:#{@agentctl_placeholder},1}") {
		t.Fatalf("cmd = %q, %v", cmd, err)
	}
	var c tmux.Client
	if err := c.Dock(context.Background(), [][]string{{"swap-pane", "-s", "%1x", "-t", "%2"}}); err == nil {
		t.Fatal("Dock accepted a malformed pane id")
	}
}
