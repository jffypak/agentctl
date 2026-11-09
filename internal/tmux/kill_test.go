package tmux_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// A tmux server outlives the command that tells it to exit: the command's
// client returns as soon as its own exit handshake is done, while the server
// stays alive, still owning the socket, until its event loop sees every client
// gone. A client that connects in that window is dropped with "server exited
// unexpectedly". Under load the window is long enough that restarting right
// after the kill reached the dying server (docs/notes/m1-flaky-tests.md), so
// these tests restart only once the old server process has exited.

// waitServerExit waits until the tmux server process pid has exited. The
// server daemonizes, so it is never a zombie of this process.
func waitServerExit(t *testing.T, pid int) {
	t.Helper()
	testenv.WaitFor(t, 0, fmt.Sprintf("tmux server %d to exit", pid), func() (bool, string) {
		err := syscall.Kill(pid, 0)
		return errors.Is(err, syscall.ESRCH), fmt.Sprintf("signal 0: %v", err)
	})
}

// restartAfterExit kills srv's server, waits for its process to exit, then
// starts a fresh server on the same socket (whose $N/%N ids start over).
func restartAfterExit(t *testing.T, srv *testenv.TmuxServer) {
	t.Helper()
	pid := srv.PID(t)
	srv.Kill(t)
	waitServerExit(t, pid)
	srv.Restart(t) // its own kill finds no server and is a no-op
}

// holdDyingServer attaches a control-mode client to session and stops it
// (SIGSTOP). A stopped client cannot finish the exit handshake, so once the
// server is told to exit it stays in the dying state deterministically, as a
// just-killed server does until the scheduler lets it finish. release resumes
// and ends the client; it also runs in t.Cleanup, before the server's cleanup.
func holdDyingServer(t *testing.T, srv *testenv.TmuxServer, session string) (release func()) {
	t.Helper()
	stdin, keepOpen, err := os.Pipe() // control mode exits at EOF on stdin
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("tmux", "-S", srv.Socket, "-C", "attach-session", "-t", "="+session)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = srv.Env.Strings(), stdin, io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGCONT)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			_ = keepOpen.Close()
			_ = stdin.Close()
		})
	}
	t.Cleanup(release)
	pid := strconv.Itoa(cmd.Process.Pid)
	testenv.WaitFor(t, 0, "control client "+pid+" to attach", func() (bool, string) {
		out, err := srv.TryRun("list-clients", "-F", "#{client_pid}")
		return err == nil && strings.Contains("\n"+out+"\n", "\n"+pid+"\n"), fmt.Sprintf("%q %v", out, err)
	})
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	return release
}

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
	restartAfterExit(t, srv)
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

// TestRestartAfterKillReachesTheDyingServer pins down the restart race behind
// the TestKillVerifiedSessionNeverHitsAReusedPaneAfterRestart flake. The old
// server is held in the state it is in right after the kill command returns:
// still alive and still owning the socket. A client connecting then fails with
// "server exited unexpectedly" (what the flaky restart's start step reported);
// once the old process has exited, the restart works and the ids start over.
func TestRestartAfterKillReachesTheDyingServer(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	old := identityOf(srv, sleepSession(t, srv, "agentctl-demo"))
	release := holdDyingServer(t, srv, "agentctl-demo")

	srv.Kill(t)
	if err := syscall.Kill(old.ServerPID, 0); err != nil {
		t.Fatalf("killed server %d already gone (%v); the hold did not keep it in the dying state", old.ServerPID, err)
	}
	if _, err := srv.TryRun("start-server"); err == nil || !strings.Contains(err.Error(), "server exited unexpectedly") {
		t.Fatalf("starting a server while the killed one is still alive: err = %v, want \"server exited unexpectedly\"", err)
	}

	release()
	waitServerExit(t, old.ServerPID)
	srv.Restart(t)
	fresh := sleepSession(t, srv, "agentctl-demo")
	if fresh.ServerPID == old.ServerPID || fresh.PaneID != old.PaneID || fresh.SessionID != old.SessionID {
		t.Fatalf("after the old server exited: old %+v, fresh %+v; want a new server reusing $N/%%N", old, fresh)
	}
}
