package tmux_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// backgroundTB lets a testenv helper run off the test goroutine: Fatal*
// records the message and ends only that goroutine (FailNow must not be
// called from it), and the test goroutine reports it.
type backgroundTB struct {
	testing.TB
	mu  sync.Mutex
	msg string
}

func (b *backgroundTB) Fatalf(format string, args ...any) {
	b.mu.Lock()
	b.msg = fmt.Sprintf(format, args...)
	b.mu.Unlock()
	runtime.Goexit()
}

func (b *backgroundTB) Fatal(args ...any) { b.Fatalf("%s", fmt.Sprint(args...)) }

func (b *backgroundTB) fatal() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.msg
}

func processAlive(pid int) bool { return !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

// dying reports whether srv's server is in its dying state: still alive, but
// dropping every new client.
func dying(srv *testenv.TmuxServer) (bool, string) {
	_, err := srv.TryRun("list-sessions")
	return err != nil && strings.Contains(err.Error(), "server exited unexpectedly"), fmt.Sprint(err)
}

// TestKillWaitsForTheOldServerToExit: testenv's Kill must not return while
// the killed server is still alive. The server is held in its dying state by
// a stopped control client; Kill may only return once that hold is released
// and the old process has exited. The old helper returned as soon as the
// kill command's client did, with the old pid alive.
func TestKillWaitsForTheOldServerToExit(t *testing.T) {
	t.Parallel()
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	old := sleepSession(t, srv, "held")
	release := holdDyingServer(t, srv, "held")

	type result struct {
		aliveAtReturn bool
		fatal         string
	}
	done := make(chan result, 1)
	bg := &backgroundTB{TB: t}
	go func() {
		defer func() { done <- result{aliveAtReturn: processAlive(old.ServerPID), fatal: bg.fatal()} }()
		srv.Kill(bg)
	}()

	testenv.WaitFor(t, 0, "the killed server to start dying", func() (bool, string) { return dying(srv) })
	select {
	case r := <-done:
		t.Fatalf("Kill returned while server %d was held in its dying state (alive at return: %v, fatal: %q)",
			old.ServerPID, r.aliveAtReturn, r.fatal)
	default:
	}
	if !processAlive(old.ServerPID) {
		t.Fatalf("server %d exited while held; the hold did not work", old.ServerPID)
	}

	release()
	var r result
	select {
	case r = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Kill did not return after the dying server was released")
	}
	if r.fatal != "" {
		t.Fatalf("Kill failed: %s", r.fatal)
	}
	if r.aliveAtReturn {
		t.Fatalf("Kill returned while the old server %d was still alive", old.ServerPID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := srv.Client().Snapshot(ctx); !errors.Is(err, tmux.ErrNoServer) {
		t.Fatalf("snapshot after Kill = %v, want ErrNoServer", err)
	}
}

// TestRestartWaitsForTheOldServerToExit: Restart must not start the new
// server until the old one has exited. While the old server is dying it still
// owns the socket, and the old helper's start-server reached it and failed
// with "server exited unexpectedly" (the m1-flaky-tests flake).
func TestRestartWaitsForTheOldServerToExit(t *testing.T) {
	t.Parallel()
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	old := sleepSession(t, srv, "held")
	release := holdDyingServer(t, srv, "held")

	type observed struct {
		sawDying, aliveWhenReleased bool
	}
	var released atomic.Bool
	seen := make(chan observed, 1)
	go func() { // releases the hold only once the kill has the server dying
		ok := testenv.Eventually(20*time.Second, func() (bool, string) { return dying(srv) })
		alive := processAlive(old.ServerPID)
		released.Store(true)
		release()
		seen <- observed{sawDying: ok, aliveWhenReleased: alive}
	}()

	srv.Restart(t)
	if !released.Load() {
		t.Fatalf("Restart returned while the old server %d was still held in its dying state", old.ServerPID)
	}
	o := <-seen
	if !o.sawDying || !o.aliveWhenReleased {
		t.Fatalf("the old server was never held in its dying state (%+v); the test proved nothing", o)
	}
	if processAlive(old.ServerPID) {
		t.Fatalf("Restart returned while the old server %d was still alive", old.ServerPID)
	}
	fresh := sleepSession(t, srv, "held")
	if fresh.ServerPID == old.ServerPID || fresh.PaneID != old.PaneID || fresh.SessionID != old.SessionID {
		t.Fatalf("after restart: old %+v, fresh %+v; want a new server reusing $N/%%N", old, fresh)
	}
}

// TestSnapshotOfADyingServerIsErrNoServer: a probe that reaches a real server
// while it is exiting reads as "no server", so reconcile records server_gone.
func TestSnapshotOfADyingServerIsErrNoServer(t *testing.T) {
	t.Parallel()
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	sleepSession(t, srv, "held")
	release := holdDyingServer(t, srv, "held")

	pid := srv.KillNoWait(t)
	if !processAlive(pid) {
		t.Fatalf("killed server %d already gone; the hold did not keep it dying", pid)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := srv.Client().Snapshot(ctx)
	if err == nil || !errors.Is(err, tmux.ErrNoServer) {
		t.Fatalf("snapshot of a dying server = %v, want ErrNoServer", err)
	}
	release()
	srv.WaitExit(t, pid)
}
