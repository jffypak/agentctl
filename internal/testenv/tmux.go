package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/tmux"
)

const tmuxCallTimeout = 10 * time.Second

// TmuxServer is an isolated tmux server on its own short -L socket
// (agentctl-test-<rand>, under $TMUX_TMPDIR/tmux-<uid>/ — not under
// t.TempDir(), which exceeds the macOS socket path limit), started with
// -f /dev/null and a hermetic environment, and killed in t.Cleanup.
type TmuxServer struct {
	Name   string // -L name
	Socket string // canonical absolute socket path (what sessions store)
	Env    Env    // the env it was started with + AGENTCTL_TMUX_SOCKET=Socket; no TMUX/TMUX_PANE
	base   Env
	pid    atomic.Int64 // pid of the server start launched; 0 once it has exited
}

// NewTmuxServer starts an isolated server. Skips when tmux is missing.
func NewTmuxServer(t testing.TB, env Env) *TmuxServer {
	t.Helper()
	RequireTool(t, "tmux")
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	s := &TmuxServer{Name: "agentctl-test-" + hex.EncodeToString(b[:]), base: env.Without("TMUX", "TMUX_PANE")}
	t.Cleanup(func() {
		_ = s.kill()
		s.removeSocketFile()
	})
	s.start(t)
	return s
}

// removeSocketFile deletes the socket tmux leaves behind after kill-server,
// but only our own agentctl-test-* socket and only if it is a socket.
func (s *TmuxServer) removeSocketFile() {
	if s.Socket == "" || filepath.Base(s.Socket) != s.Name {
		return
	}
	if fi, err := os.Lstat(s.Socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(s.Socket)
	}
}

func (s *TmuxServer) start(t testing.TB) {
	t.Helper()
	r := tmux.ExecRunner{Env: s.base.Strings(), ConfigFile: "/dev/null"}
	ctx, cancel := context.WithTimeout(context.Background(), tmuxCallTimeout)
	defer cancel()
	if _, err := r.Run(ctx, tmux.Request{
		Socket:   tmux.Socket{Name: s.Name},
		Commands: [][]string{{"start-server"}, {"set-option", "-g", "exit-empty", "off"}},
	}); err != nil {
		t.Fatalf("start tmux server %s: %v", s.Name, err)
	}
	sock, err := tmux.NewClient(r, tmux.Socket{Name: s.Name}).SocketPath(ctx)
	if err != nil {
		t.Fatalf("tmux server %s socket path: %v", s.Name, err)
	}
	pid, err := tmux.NewClient(r, tmux.Socket{Name: s.Name}).ServerPID(ctx)
	if err != nil {
		t.Fatalf("tmux server %s pid: %v", s.Name, err)
	}
	s.pid.Store(int64(pid))
	s.Socket = sock
	s.Env = s.base.With(tmux.EnvSocket + "=" + sock)
}

// Runner returns an exec runner with the server's hermetic env and -f /dev/null.
func (s *TmuxServer) Runner() tmux.Runner {
	return tmux.ExecRunner{Env: s.Env.Strings(), ConfigFile: "/dev/null"}
}

// Client returns a client bound to the server's socket path.
func (s *TmuxServer) Client() tmux.Client {
	return tmux.NewClient(s.Runner(), tmux.Socket{Path: s.Socket})
}

// Run runs one tmux command against the server and returns trimmed stdout.
func (s *TmuxServer) Run(t testing.TB, args ...string) string {
	t.Helper()
	out, err := s.TryRun(args...)
	if err != nil {
		t.Fatalf("tmux %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// TryRun is Run without failing the test.
func (s *TmuxServer) TryRun(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxCallTimeout)
	defer cancel()
	res, err := s.Runner().Run(ctx, tmux.Request{Socket: tmux.Socket{Path: s.Socket}, Commands: [][]string{args}})
	return strings.TrimSpace(string(res.Stdout)), err
}

// PID is the current server pid.
func (s *TmuxServer) PID(t testing.TB) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), tmuxCallTimeout)
	defer cancel()
	pid, err := s.Client().ServerPID(ctx)
	if err != nil {
		t.Fatalf("tmux server pid: %v", err)
	}
	return pid
}

// Snapshot returns the server pid and all panes.
func (s *TmuxServer) Snapshot(t testing.TB) tmux.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), tmuxCallTimeout)
	defer cancel()
	snap, err := s.Client().Snapshot(ctx)
	if err != nil {
		t.Fatalf("tmux snapshot: %v", err)
	}
	return snap
}

// Kill stops the server (simulating a crash or a user's kill-server) and
// returns only once the old server process has exited. The socket path stays
// the same, so Restart can bring up a new server whose pane ids (%0, %1, ...)
// collide with the old ones.
//
// The wait matters: kill-server's client returns as soon as its own exit
// handshake is done, while the server lives on, still owning the socket and
// dropping new clients ("server exited unexpectedly"), until every client is
// gone. A start or probe in that window reaches the dying server
// (docs/notes/m1-flaky-tests.md, docs/notes/m2-hardening-followups.md).
func (s *TmuxServer) Kill(t testing.TB) {
	t.Helper()
	if err := s.kill(); err != nil {
		t.Fatalf("kill tmux server: %v", err)
	}
}

// Restart kills the server, waits for the old process to exit, and starts a
// fresh one on the same socket.
func (s *TmuxServer) Restart(t testing.TB) {
	t.Helper()
	s.Kill(t)
	s.start(t)
}

// KillNoWait tells the server to exit and returns its pid without waiting
// for the process to go away, for tests that observe the dying server. Pair
// it with WaitExit before restarting; Kill and Restart also wait for it.
func (s *TmuxServer) KillNoWait(t testing.TB) (pid int) {
	t.Helper()
	pid, err := s.sendKill()
	if err != nil {
		t.Fatalf("kill tmux server: %v", err)
	}
	return pid
}

// WaitExit waits until the tmux server process pid has exited.
func (s *TmuxServer) WaitExit(t testing.TB, pid int) {
	t.Helper()
	if err := s.waitExit(pid); err != nil {
		t.Fatal(err)
	}
}

// kill is sendKill then waitExit.
func (s *TmuxServer) kill() error {
	pid, err := s.sendKill()
	if err != nil {
		return err
	}
	return s.waitExit(pid)
}

// sendKill runs kill-server and returns the pid of the server it was sent to
// (0 when none is running). A server that no longer answers may be dying
// already, so the pid falls back to the one this TmuxServer last started.
func (s *TmuxServer) sendKill() (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxCallTimeout)
	defer cancel()
	pid, err := tmux.NewClient(tmux.ExecRunner{Env: s.base.Strings()}, tmux.Socket{Name: s.Name}).ServerPID(ctx)
	switch {
	case err == nil:
	case errors.Is(err, tmux.ErrNoServer):
		pid = int(s.pid.Load())
	default:
		return 0, fmt.Errorf("server pid: %w", err)
	}
	r := tmux.ExecRunner{Env: s.base.Strings()}
	_, err = r.Run(ctx, tmux.Request{Socket: tmux.Socket{Name: s.Name}, Commands: [][]string{{"kill-server"}}})
	if err != nil && !errors.Is(err, tmux.ErrNoServer) {
		return 0, err
	}
	return pid, nil
}

// waitExit polls until pid has exited (signal 0 reports ESRCH). The server
// daemonizes, so it is never a zombie of this process. It is bounded by
// tmuxCallTimeout.
func (s *TmuxServer) waitExit(pid int) error {
	if pid <= 0 {
		return nil
	}
	var last error
	if !Eventually(tmuxCallTimeout, func() (bool, string) {
		last = syscall.Kill(pid, 0)
		return errors.Is(last, syscall.ESRCH), ""
	}) {
		return fmt.Errorf("tmux server %d still running %v after kill-server (signal 0: %v)", pid, tmuxCallTimeout, last)
	}
	s.pid.CompareAndSwap(int64(pid), 0)
	return nil
}
