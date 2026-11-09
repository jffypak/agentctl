package testenv

import (
	"context"
	"testing"
)

func TestControlClientFollowsSwitchClient(t *testing.T) {
	t.Parallel()
	srv := NewTmuxServer(t, NewEnv(t))
	srv.Run(t, "new-session", "-d", "-s", "home", "--", "sleep", "600")
	srv.Run(t, "new-session", "-d", "-s", "agent", "--", "sleep", "600")
	pane := srv.Run(t, "display-message", "-p", "-t", "=agent:", "#{pane_id}")

	c := srv.AttachControlClient(t, "=home")
	if got := c.Session(t); got != "home" {
		t.Fatalf("client session = %q, want home", got)
	}
	if err := srv.Client().SwitchClient(context.Background(), pane); err != nil {
		t.Fatalf("switch-client: %v", err)
	}
	WaitFor(t, 0, "client on the agent session", func() (bool, string) {
		got := c.Session(t)
		return got == "agent", got
	})
	c.Close()
	WaitFor(t, 0, "client detached", func() (bool, string) {
		out, _ := srv.TryRun("list-clients", "-F", "#{client_session}")
		return out == "", out
	})
	for _, s := range []string{"home", "agent"} {
		if _, err := srv.TryRun("has-session", "-t", "="+s); err != nil {
			t.Fatalf("session %s gone after the client detached: %v", s, err)
		}
	}
}
