package hookingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// pasted enqueues body and moves it to sent, as Drain would.
func (f *fixture) pasted(body string) store.Message {
	f.t.Helper()
	ctx := context.Background()
	m, err := f.st.Messages().Enqueue(ctx, store.NewMessage{TaskID: f.sess.TaskID, SessionID: f.sess.ID, Body: body})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, ok, err := f.st.Messages().Claim(ctx, f.sess.ID, 0); !ok || err != nil {
		f.t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err := f.st.Messages().MarkSent(ctx, m.ID, time.Now(), 0); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *fixture) message(id int64) store.Message {
	f.t.Helper()
	m, err := f.st.Messages().Get(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return m
}

func TestAckFollowupByHashThenByTiming(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateReady)
	m := f.pasted("run the tests")
	r := f.run(f.env("demo"), payload("UserPromptSubmit", "prompt", "run the tests"))
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	got := f.message(m.ID)
	if got.State != domain.MsgAcknowledged || got.AckMatch != domain.AckHash {
		t.Fatalf("message = %+v", got)
	}
	acks := f.events(domain.EventMessageAcknowledged)
	recv := f.events(domain.EventHookReceived)
	if len(acks) != 1 || acks[0].CauseID != recv[len(recv)-1].ID {
		t.Fatalf("acknowledged cause = %+v, want the hook.received %d", acks, recv[len(recv)-1].ID)
	}

	// placeholder mode: the prompt is "[Pasted text #1]", acknowledged by timing
	for _, ev := range []string{"Stop"} {
		f.run(f.env("demo"), payload(ev))
	}
	m2 := f.pasted("a very long multi-line paste\nline 2")
	f.run(f.env("demo"), payload("UserPromptSubmit", "prompt", "[Pasted text #1]"))
	if got := f.message(m2.ID); got.State != domain.MsgAcknowledged || got.AckMatch != domain.AckTiming {
		t.Fatalf("placeholder message = %+v", got)
	}
}

func TestTriggerSpawnsDeliverOnlyOnReadyWithQueue(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateWorking)
	var mu sync.Mutex
	var calls [][2]int64
	spawner := SpawnerFunc(func(id int64, pid int) error {
		mu.Lock()
		calls = append(calls, [2]int64{id, int64(pid)})
		mu.Unlock()
		return nil
	})
	withSpawner := func(c *Config) { c.Spawner, c.PID = spawner, 4321 }

	f.run(f.env("demo"), payload("Stop"), withSpawner) // ready, nothing queued
	if len(calls) != 0 {
		t.Fatalf("spawned without a queue: %v", calls)
	}
	f.run(f.env("demo"), payload("UserPromptSubmit", "prompt", "x"), withSpawner)
	if _, err := f.st.Messages().Enqueue(context.Background(), store.NewMessage{TaskID: "demo", SessionID: f.sess.ID, Body: "queued"}); err != nil {
		t.Fatal(err)
	}
	f.run(f.env("demo"), payload("PreToolUse", "tool_name", "Bash"), withSpawner) // working: no trigger
	r := f.run(f.env("demo"), payload("Stop"), withSpawner)
	if r.Err != nil || len(calls) != 1 || calls[0] != [2]int64{f.sess.ID, 4321} {
		t.Fatalf("calls = %v (err %v)", calls, r.Err)
	}
	// ready -> ready (idle_prompt) is not a new ready transition
	f.run(f.env("demo"), payload("Notification", "notification_type", "idle_prompt", "message", "idle"), withSpawner)
	if len(calls) != 1 {
		t.Fatalf("self move triggered: %v", calls)
	}
	// a failing spawner is logged, never undoes the record
	f.run(f.env("demo"), payload("UserPromptSubmit", "prompt", "y"), withSpawner)
	r = f.run(f.env("demo"), payload("Stop"), func(c *Config) {
		c.Spawner = SpawnerFunc(func(int64, int) error { return errors.New("no exec") })
	})
	if f.session().State != domain.StateReady {
		t.Fatalf("state = %s", f.session().State)
	}
	if r.Err != nil {
		t.Fatalf("outcome err = %v (followup errors are logged only)", r.Err)
	}
}

func lockDB(t *testing.T, st *store.Store) func() {
	t.Helper()
	conn, err := st.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		_ = conn.Close()
	}
}

// TestLostHookMarker: a hook whose write fails because the DB stays locked
// past its busy timeout (or past its budget) leaves a marker; the next
// recorded hook with a state clears it.
func TestLostHookMarker(t *testing.T) {
	for name, mod := range map[string]func(*Config){
		"busy timeout": func(c *Config) { c.BusyTimeout, c.Budget = 100*time.Millisecond, 1500*time.Millisecond },
		"budget":       func(c *Config) { c.BusyTimeout, c.Budget = 1500*time.Millisecond, 200*time.Millisecond },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, domain.OriginLaunched, domain.StateReady)
			marker := filepath.Join(f.p.Hooks, LostDirName, domain.LostHookSessionKey(f.sess.ID))
			unlock := lockDB(t, f.st)
			start := time.Now()
			r := f.run(f.env("demo"), payload("UserPromptSubmit", "prompt", "typed by hand"), mod)
			if r.Err == nil || time.Since(start) > 2*time.Second {
				t.Fatalf("outcome = %+v after %v", r.Outcome, time.Since(start))
			}
			unlock()
			fi, err := os.Stat(marker)
			if err != nil {
				t.Fatalf("no marker: %v\nerror log: %s", err, f.errorLog())
			}
			if fi.Mode().Perm() != 0o600 {
				t.Fatalf("marker mode = %v", fi.Mode())
			}
			b, _ := os.ReadFile(marker)
			var m lostMarker
			if err := json.Unmarshal(b, &m); err != nil || m.TSMillis == 0 || m.Event != "UserPromptSubmit" || m.Session != f.sess.ID {
				t.Fatalf("marker = %s (%v)", b, err)
			}
			if !time.UnixMilli(m.TSMillis).After(f.session().StateSince) {
				t.Fatal("marker must be newer than the stale ready state")
			}
			if s := f.session(); s.State != domain.StateReady {
				t.Fatalf("the lost submit changed state: %s", s.State)
			}
			// a record-only hook does not clear it; the next state-setting one does
			f.run(f.env("demo"), payload("SubagentStop"))
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("a record-only hook cleared the marker")
			}
			f.run(f.env("demo"), payload("PreToolUse", "tool_name", "Bash"))
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marker not cleared: %v", err)
			}
		})
	}
}

func TestLostHookMarkerKeysAndNoHome(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateReady)
	env := f.env("demo")
	if k := lostKey(0, adapterEnv(env)); k != "task-demo" {
		t.Fatalf("task key = %q", k)
	}
	delete(env, "AGENTCTL_TASK_ID")
	if k := lostKey(0, adapterEnv(env)); k != "pane-3" {
		t.Fatalf("pane key = %q", k)
	}
	gone := filepath.Join(t.TempDir(), "missing-home")
	p := f.p
	p.Home, p.Hooks = gone, filepath.Join(gone, "hooks")
	if err := writeLostMarker(p, time.Now(), Outcome{SessionID: 1}, adapterEnv(env), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(gone); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a marker created the agentctl home")
	}
}

func adapterEnv(env map[string]string) adapter.HookEnv {
	return adapter.HookEnv{TaskID: env["AGENTCTL_TASK_ID"], Home: env["AGENTCTL_HOME"], TMUX: env["TMUX"], TMUXPane: env["TMUX_PANE"]}
}
