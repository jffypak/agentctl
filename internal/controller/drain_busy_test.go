package controller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
)

// busyTimeout is the registry write budget of these tests: short, so holding
// the write lock past it is cheap.
const busyTimeout = 200 * time.Millisecond

// busyRig is a pqRig whose controller waits at most busyTimeout for the
// write lock, and a way to hold that lock from another connection, as a
// burst of concurrent writers (other drains, hooks, the TUI) does under load.
func busyRig(t *testing.T) *pqRig {
	t.Helper()
	r := newPQRig(t, pqTunables())
	ctl, err := Open(context.Background(), Config{Environ: r.env.Strings(), AgentctlPath: "/opt/agentctl/bin/agentctl",
		Tunables: pqTunables(), BusyTimeout: busyTimeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctl.Close() })
	r.ctl = ctl
	return r
}

// holdWriteLock takes the registry's write lock until the returned func runs.
func holdWriteLock(t *testing.T, r *pqRig) func() {
	t.Helper()
	conn, err := r.ctl.store.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

func (r *pqRig) enqueue(body string) store.Message {
	r.t.Helper()
	m, err := r.ctl.store.Messages().Enqueue(context.Background(), store.NewMessage{TaskID: "demo", SessionID: r.sess.ID, Body: body})
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

// TestDrainWithNothingToWriteTakesNoWriteLock: a drain that cannot deliver
// (agent working; one already delivered this turn) and has nothing stale to
// sweep only reads. It used to take the write lock twice (sweep + claim), so
// with the lock held past the busy timeout it failed with SQLITE_BUSY.
func TestDrainWithNothingToWriteTakesNoWriteLock(t *testing.T) {
	t.Parallel()
	r := busyRig(t)
	ctx := context.Background()
	r.hook(domain.StateWorking)
	r.enqueue("waits")
	// The write lock is held throughout: a drain that tried to write would
	// come back ReasonRegistryBusy, not the read-decided working label. The
	// proof is behavioural; a wall-clock bound here flaked under load.
	release := holdWriteLock(t, r)
	dr, err := r.ctl.Drain(ctx, r.sess.ID)
	if err != nil || dr.Reason != domain.LabelWorking {
		t.Fatalf("drain while working = %+v %v", dr, err)
	}
	if err := r.ctl.DrainAll(ctx); err != nil {
		t.Fatalf("DrainAll = %v", err)
	}
	release()

	// ready, one message already pasted this turn: the claim is decided by a read
	r.hook(domain.StateReady)
	if dr, err := r.ctl.Drain(ctx, r.sess.ID); err != nil || !dr.Delivered {
		t.Fatalf("delivery = %+v %v", dr, err)
	}
	r.enqueue("next turn")
	release = holdWriteLock(t, r)
	defer release()
	if dr, err := r.ctl.Drain(ctx, r.sess.ID); err != nil || dr.Reason != ReasonOnePerTurn {
		t.Fatalf("drain in the same turn = %+v %v", dr, err)
	}
}

// TestDrainBurstPastTheBusyTimeoutStaysQueued is the flake's mechanism made
// deterministic: 20 concurrent drains of one ready session while the write
// lock stays taken past the busy timeout (a writer queue longer than the
// budget, as on a loaded machine). Each claim fails with SQLITE_BUSY: that
// must read as "not claimed this time" (the message stays queued, nothing is
// pasted), not as an error. Once the lock is free, exactly one is delivered.
func TestDrainBurstPastTheBusyTimeoutStaysQueued(t *testing.T) {
	t.Parallel()
	r := busyRig(t)
	ctx := context.Background()
	first, second := r.enqueue("one"), r.enqueue("two")
	burst := func() (delivered int, reasons map[string]int) {
		reasons = map[string]int{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dr, err := r.ctl.Drain(ctx, r.sess.ID)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					t.Errorf("drain: %v", err)
				}
				if dr.Delivered {
					delivered++
				}
				reasons[dr.Reason]++
			}()
		}
		wg.Wait()
		return delivered, reasons
	}
	release := holdWriteLock(t, r)
	if n, reasons := burst(); n != 0 || reasons[ReasonRegistryBusy] != 20 {
		t.Fatalf("burst under the lock: %d delivered, reasons %v", n, reasons)
	}
	for _, m := range []store.Message{first, second} {
		if got := r.message(m.ID).State; got != domain.MsgQueued {
			t.Fatalf("message %d = %s, want queued", m.ID, got)
		}
	}
	if got := readInputs(t, r.log); len(got) != 0 {
		t.Fatalf("pasted: %+v", got)
	}
	release()
	if n, reasons := burst(); n != 1 {
		t.Fatalf("burst after release: %d delivered, reasons %v", n, reasons)
	}
	r.waitPastes(1)
	if got := r.pastes(); len(got) != 1 || got[0].Text != "one" || r.message(second.ID).State != domain.MsgQueued {
		t.Fatalf("pastes %+v, second %s", got, r.message(second.ID).State)
	}
}

// TestDeliverRetriesABusyClaim: the hook-spawned deliver (often the only
// drainer) retries a claim that found the registry busy, so a write burst
// at Stop delays the paste instead of dropping it to the next turn. The lock
// is released only after deliver has logged its first busy retry.
func TestDeliverRetriesABusyClaim(t *testing.T) {
	t.Parallel()
	r := busyRig(t)
	m := r.enqueue("after the burst")
	release := holdWriteLock(t, r)
	log := &syncLog{}
	go func() {
		testenv.Eventually(10*time.Second, func() (bool, string) {
			return strings.Contains(log.String(), "registry busy; drain retry 1/"), ""
		})
		release()
	}()
	rep := r.ctl.DeliverAfterHook(context.Background(), DeliverRequest{SessionID: r.sess.ID, Log: log})
	if !rep.Drain.Delivered || !strings.Contains(log.String(), "registry busy; drain retry 1/") {
		t.Fatalf("report = %+v\n%s", rep, log.String())
	}
	r.waitPastes(1)
	if got := r.message(m.ID).State; got != domain.MsgUnconfirmed && got != domain.MsgSent {
		t.Fatalf("message = %s", got) // no hooks in this rig: no ack
	}
}

// syncLog is a deliver log the test reads while deliver writes it.
type syncLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
