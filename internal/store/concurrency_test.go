package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/jffypak/agentctl/internal/domain"
)

const (
	helperEnvDB      = "AGENTCTL_STORE_HELPER_DB"
	helperEnvSession = "AGENTCTL_STORE_HELPER_SESSION"
	helperEnvShared  = "AGENTCTL_STORE_HELPER_SHARED"
	writerIterations = 40
)

// writerLoop appends events and toggles its own session plus a shared session
// between working and ready. Every iteration is two write transactions.
func writerLoop(ctx context.Context, st *Store, own, shared int64, tag string) error {
	for i := 0; i < writerIterations; i++ {
		if _, err := st.Events().Append(ctx, EventInput{Type: domain.EventHookReceived, SessionID: own, Payload: map[string]any{"w": tag, "i": i}}); err != nil {
			return fmt.Errorf("%s append %d: %w", tag, i, err)
		}
		to := domain.StateWorking
		if i%2 == 1 {
			to = domain.StateReady
		}
		if _, err := st.Sessions().Transition(ctx, own, to, domain.ConfidenceHook, tag, domain.SourceHook); err != nil {
			return fmt.Errorf("%s transition own %d: %w", tag, i, err)
		}
		if _, err := st.Sessions().Transition(ctx, shared, to, domain.ConfidenceHook, tag, domain.SourceHook); err != nil {
			return fmt.Errorf("%s transition shared %d: %w", tag, i, err)
		}
	}
	return nil
}

// isBusyOrLocked counts any lock failure, including one only visible in a
// helper process's output text.
func isBusyOrLocked(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()&0xff == sqlite3.SQLITE_BUSY || se.Code()&0xff == sqlite3.SQLITE_LOCKED
	}
	return strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked")
}

// TestHelperStoreWriter is not a real test: it is the body of the OS-process
// writers spawned by TestConcurrentWritersNoBusy.
func TestHelperStoreWriter(t *testing.T) {
	dbPath := os.Getenv(helperEnvDB)
	if dbPath == "" {
		t.Skip("helper process only")
	}
	own, _ := strconv.ParseInt(os.Getenv(helperEnvSession), 10, 64)
	shared, _ := strconv.ParseInt(os.Getenv(helperEnvShared), 10, 64)
	// Hook processes open with the shorter hook busy timeout.
	st, err := Open(context.Background(), dbPath, Options{BusyTimeout: 1500 * time.Millisecond})
	if err != nil {
		fmt.Fprintf(os.Stderr, "HELPER-ERROR open: %v busy=%v\n", err, isBusyOrLocked(err))
		os.Exit(3)
	}
	defer st.Close()
	if err := writerLoop(context.Background(), st, own, shared, "proc"+os.Getenv(helperEnvSession)); err != nil {
		fmt.Fprintf(os.Stderr, "HELPER-ERROR %v busy=%v\n", err, isBusyOrLocked(err))
		os.Exit(4)
	}
	fmt.Println("HELPER-OK")
}

func TestConcurrentWritersNoBusy(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	st, path := openTest(t)
	ctx := context.Background()
	const goroutines, procs = 8, 2
	var sessions []int64
	for i := 0; i < goroutines+procs+1; i++ {
		id := fmt.Sprintf("t%d", i)
		seedTask(t, st, id)
		sessions = append(sessions, seedSession(t, st, id, domain.StateStarting).ID)
	}
	shared := sessions[len(sessions)-1]

	var cmds []*exec.Cmd
	var outs []*strings.Builder
	for p := 0; p < procs; p++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperStoreWriter$", "-test.count=1")
		cmd.Env = append(os.Environ(),
			helperEnvDB+"="+path,
			helperEnvSession+"="+strconv.FormatInt(sessions[goroutines+p], 10),
			helperEnvShared+"="+strconv.FormatInt(shared, 10),
		)
		out := &strings.Builder{}
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
		outs = append(outs, out)
	}

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			s := st
			if g%2 == 1 { // half the goroutines use their own pool, like separate processes
				own, err := Open(ctx, path, Options{})
				if err != nil {
					errs <- err
					return
				}
				defer own.Close()
				s = own
			}
			if err := writerLoop(ctx, s, sessions[g], shared, "g"+strconv.Itoa(g)); err != nil {
				errs <- err
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	busy := 0
	for err := range errs {
		if isBusyOrLocked(err) {
			busy++
		}
		t.Errorf("goroutine writer: %v", err)
	}
	for i, cmd := range cmds {
		err := cmd.Wait()
		if err != nil || !strings.Contains(outs[i].String(), "HELPER-OK") {
			if strings.Contains(outs[i].String(), "busy=true") {
				busy++
			}
			t.Errorf("process writer %d: %v\n%s", i, err, outs[i].String())
		}
	}
	if busy != 0 {
		t.Fatalf("%d writers hit SQLITE_BUSY", busy)
	}

	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE type = ?`, domain.EventHookReceived).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if want := (goroutines + procs) * writerIterations; n != want {
		t.Fatalf("hook.received events = %d, want %d", n, want)
	}
	for _, id := range sessions[:goroutines+procs] {
		s, err := st.Sessions().Get(ctx, id)
		if err != nil || s.State != domain.StateReady {
			t.Fatalf("session %d = %s %v, want ready", id, s.State, err)
		}
	}
}
