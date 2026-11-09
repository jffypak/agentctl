package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

// sqliteCode is the extended SQLite result code of err, or -1.
func sqliteCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return -1
}

// Root cause of the TestConcurrentWritersNoBusy flake (docs/notes/m1-flaky-tests.md):
// a writer waiting for the write lock only re-probes on SQLite's busy-handler
// back-off (1, 2, 5 ... 100 ms sleeps: about 25 probes in the hook's 1.5 s),
// while a writer that just committed takes the lock again at once. When
// writers keep the lock busy back to back, the waiting hook writer can miss
// every free moment and fail with SQLITE_BUSY after its whole timeout even
// though the lock changed hands hundreds of times.
//
// lockHold models a transaction that holds the write lock for a few ms, as
// every transaction does under CPU load with -race; it is how long the lock is
// held, not a wait for a condition.
const lockHold = 10 * time.Millisecond

// hookWrites is how many append+transition pairs the hook writer makes, each
// starting cold against the back-to-back writers.
const hookWrites = 8

// backToBackWriter runs write transactions on st one right after another,
// each holding the write lock for lockHold, until stop is closed.
func backToBackWriter(st *Store, sess int64, stop <-chan struct{}, commits *atomic.Int64) error {
	ctx := context.Background()
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		err := st.withTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE sessions SET last_detail = ? WHERE id = ?`, "busy", sess); err != nil {
				return err
			}
			time.Sleep(lockHold)
			return nil
		})
		if err != nil {
			return err
		}
		commits.Add(1)
	}
}

func TestHookWriterIsNotStarvedByBackToBackWriters(t *testing.T) {
	st, path := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "busy")
	busySess := seedSession(t, st, "busy", domain.StateStarting).ID
	seedTask(t, st, "hook")
	hookSess := seedSession(t, st, "hook", domain.StateStarting).ID

	other, err := Open(ctx, path, Options{}) // a second long-lived writer, like another process
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	hook, err := Open(ctx, path, Options{BusyTimeout: HookBusyTimeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hook.Close() })

	stop := make(chan struct{})
	var commits atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, w := range []*Store{st, st, other} {
		wg.Add(1)
		go func(w *Store) {
			defer wg.Done()
			if err := backToBackWriter(w, busySess, stop, &commits); err != nil {
				errs <- err
			}
		}(w)
	}
	stopWriters := func() {
		close(stop)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("back-to-back writer: %v", err)
		}
	}
	// Each hook write starts while the others are committing back to back
	// again: once the hook writer holds the lock, the others back off into
	// longer busy-handler sleeps and the hook's next write would get in free.
	saturated := func(what string) {
		from := commits.Load()
		testenv.WaitFor(t, 0, what, func() (bool, string) {
			n := commits.Load() - from
			return n >= 5, fmt.Sprintf("%d commits", n)
		})
	}

	var failures []string
	for i := 0; i < hookWrites; i++ {
		saturated("back-to-back writers to commit before append")
		before, start := commits.Load(), time.Now()
		if _, err := hook.Events().Append(ctx, EventInput{Type: domain.EventHookReceived, SessionID: hookSess}); err != nil {
			failures = append(failures, fmt.Sprintf("append %d: %v (code %d) after %v; the others committed %d times meanwhile",
				i, err, sqliteCode(err), time.Since(start).Round(time.Millisecond), commits.Load()-before))
		}
		to := domain.StateWorking
		if i%2 == 1 {
			to = domain.StateReady
		}
		saturated("back-to-back writers to commit before transition")
		before, start = commits.Load(), time.Now()
		if _, err := hook.Sessions().Transition(ctx, hookSess, to, domain.ConfidenceHook, "hook", domain.SourceHook); err != nil {
			failures = append(failures, fmt.Sprintf("transition %d: %v (code %d) after %v; the others committed %d times meanwhile",
				i, err, sqliteCode(err), time.Since(start).Round(time.Millisecond), commits.Load()-before))
		}
	}
	stopWriters()
	for _, f := range failures {
		t.Errorf("hook writer starved: %s", f)
	}
}
