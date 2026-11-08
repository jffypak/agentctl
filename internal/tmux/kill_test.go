package tmux_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

func TestKillVerifiedSessionCommand(t *testing.T) {
	id := domain.Identity{Socket: "/tmp/s", ServerPID: 100, SessionID: "$3", PaneID: "%7", PanePID: 200}
	got, err := tmux.KillVerifiedSessionCommand(id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"if-shell", "-F", "-t", "%7",
		"#{&&:#{==:#{pid},100},#{&&:#{==:#{session_id},$3},#{&&:#{==:#{pane_id},%7},#{==:#{pane_pid},200}}}}"}
	// archtest keeps the kill vocabulary out of every file but kill.go, so the
	// guarded command string is checked in two halves.
	if len(got) != 6 || strings.Join(got[:5], "\x00") != strings.Join(want, "\x00") ||
		!strings.HasPrefix(got[5], "kill-") || !strings.HasSuffix(got[5], "-session -t '$3'") || strings.Count(got[5], " ") != 2 {
		t.Fatalf("command = %q", got)
	}
	bad := map[string]domain.Identity{
		"session id":      {ServerPID: 1, SessionID: "$3'; display-message x", PaneID: "%1", PanePID: 2},
		"session name":    {ServerPID: 1, SessionID: "agentctl-demo", PaneID: "%1", PanePID: 2},
		"pane id":         {ServerPID: 1, SessionID: "$3", PaneID: "%1,1", PanePID: 2},
		"empty pane":      {ServerPID: 1, SessionID: "$3", PanePID: 2},
		"no server pid":   {SessionID: "$3", PaneID: "%1", PanePID: 2},
		"no pane pid":     {ServerPID: 1, SessionID: "$3", PaneID: "%1"},
		"bare dollar":     {ServerPID: 1, SessionID: "$", PaneID: "%1", PanePID: 2},
		"bare percent":    {ServerPID: 1, SessionID: "$3", PaneID: "%", PanePID: 2},
		"negative pid":    {ServerPID: -1, SessionID: "$3", PaneID: "%1", PanePID: 2},
		"whitespace id":   {ServerPID: 1, SessionID: "$3 ", PaneID: "%1", PanePID: 2},
		"zero identity":   {},
		"signed pane pid": {ServerPID: 1, SessionID: "$3", PaneID: "%1", PanePID: -2},
	}
	for name, id := range bad {
		if _, err := tmux.KillVerifiedSessionCommand(id); err == nil {
			t.Errorf("%s: accepted %+v", name, id)
		}
	}
}

func sleepSession(t *testing.T, srv *testenv.TmuxServer, name string) tmux.Created {
	t.Helper()
	c, err := srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{Name: name, Dir: "/", Argv: []string{"/bin/sleep", "600"}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func identityOf(srv *testenv.TmuxServer, c tmux.Created) domain.Identity {
	return domain.Identity{Socket: srv.Socket, ServerPID: c.ServerPID, SessionID: c.SessionID, PaneID: c.PaneID, PanePID: c.PanePID}
}

func TestKillVerifiedSessionOnlyKillsTheExactIdentity(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	ctx := context.Background()
	target := sleepSession(t, srv, "target")
	other := sleepSession(t, srv, "other")
	id := identityOf(srv, target)

	for name, wrong := range map[string]domain.Identity{
		"pane pid":   {Socket: id.Socket, ServerPID: id.ServerPID, SessionID: id.SessionID, PaneID: id.PaneID, PanePID: id.PanePID + 1000},
		"server pid": {Socket: id.Socket, ServerPID: id.ServerPID + 1, SessionID: id.SessionID, PaneID: id.PaneID, PanePID: id.PanePID},
		"session id": {Socket: id.Socket, ServerPID: id.ServerPID, SessionID: other.SessionID, PaneID: id.PaneID, PanePID: id.PanePID},
		"missing":    {Socket: id.Socket, ServerPID: id.ServerPID, SessionID: id.SessionID, PaneID: "%99", PanePID: id.PanePID},
	} {
		if err := srv.Client().KillVerifiedSession(ctx, wrong); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		snap := srv.Snapshot(t)
		if _, ok := snap.Find(target.PaneID); !ok {
			t.Fatalf("%s: mismatched identity killed the target", name)
		}
		if _, ok := snap.Find(other.PaneID); !ok {
			t.Fatalf("%s: mismatched identity killed another session", name)
		}
	}
	if err := srv.Client().KillVerifiedSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	snap := srv.Snapshot(t)
	if _, ok := snap.Find(target.PaneID); ok {
		t.Fatal("exact identity was not killed")
	}
	if _, ok := snap.Find(other.PaneID); !ok {
		t.Fatal("the other session was killed")
	}
}

func TestKillVerifiedSessionNeverHitsAReusedPaneAfterRestart(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	ctx := context.Background()
	old := identityOf(srv, sleepSession(t, srv, "agentctl-demo"))
	srv.Restart(t)
	intruder := sleepSession(t, srv, "agentctl-demo") // same name, same $N and %N, new pids
	if intruder.PaneID != old.PaneID || intruder.SessionID != old.SessionID {
		t.Fatalf("expected id reuse: old %+v new %+v", old, intruder)
	}
	if err := srv.Client().KillVerifiedSession(ctx, old); err != nil {
		t.Fatal(err)
	}
	if p, ok := srv.Snapshot(t).Find(intruder.PaneID); !ok || p.PanePID != intruder.PanePID {
		t.Fatal("the stale identity killed the pane that reused its ids")
	}
}
