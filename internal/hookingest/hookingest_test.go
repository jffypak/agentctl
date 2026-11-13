package hookingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
)

const serverPID = 4242

// fixture is a registry with one running task whose session is bound to pane
// %3 on a fake socket served by pid 4242. No tmux is involved: hookingest
// only compares the hook's $TMUX/$TMUX_PANE with the stored identity.
type fixture struct {
	t      *testing.T
	p      paths.Paths
	st     *store.Store
	socket string
	sess   store.Session
	goal   string // the seeded tasks' goal ("" = launched without one)
}

func newFixture(t *testing.T, origin domain.Origin, state domain.AgentState) *fixture {
	t.Helper()
	return newGoalFixture(t, origin, state, "")
}

// newGoalFixture is newFixture whose task was created with goal.
func newGoalFixture(t *testing.T, origin domain.Origin, state domain.AgentState, goal string) *fixture {
	t.Helper()
	p := paths.ForHome(testenv.TempHome(t))
	if err := paths.EnsureHome(p); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	sock := filepath.Join(p.Home, "tmux-sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, p: p, st: st, socket: sock, goal: goal}
	f.sess = f.seed("demo", origin, state, "%3")
	return f
}

func (f *fixture) seed(id string, origin domain.Origin, state domain.AgentState, pane string) store.Session {
	f.t.Helper()
	ctx := context.Background()
	repo, err := f.st.Repos().Upsert(ctx, "/repos/main", "main")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.st.Tasks().Reserve(ctx, store.Task{ID: id, RepoID: repo.ID, Name: id, Goal: f.goal, WorktreePath: "/w/" + id, Branch: "jpark/" + id}); err != nil {
		f.t.Fatal(err)
	}
	initial := state
	if state != domain.StateStarting && state != domain.StateUnknown {
		initial = domain.StateStarting
	}
	sess, err := f.st.Sessions().Create(ctx, store.NewSession{
		TaskID: id, AgentType: "claude", Origin: origin, State: initial, Confidence: domain.ConfidenceNone,
		Identity: domain.Identity{Socket: f.socket, ServerPID: serverPID, SessionID: "$0", PaneID: pane, PanePID: 77},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if state != initial {
		if _, err := f.st.Sessions().Transition(ctx, sess.ID, state, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
			f.t.Fatal(err)
		}
		sess.State = state
	}
	return sess
}

// env is the environment a hook in the session's pane inherits.
func (f *fixture) env(task string) map[string]string {
	e := map[string]string{
		"AGENTCTL_HOME": f.p.Home,
		"TMUX":          f.socket + "," + "4242" + ",0",
		"TMUX_PANE":     "%3",
	}
	if task != "" {
		e["AGENTCTL_TASK_ID"] = task
	}
	return e
}

type runResult struct {
	Outcome
	stderr  string
	elapsed time.Duration
}

func (f *fixture) run(env map[string]string, stdin io.Reader, mods ...func(*Config)) runResult {
	f.t.Helper()
	var errb bytes.Buffer
	cfg := Config{
		Getenv: func(k string) string { return env[k] },
		Stdin:  stdin, Stderr: &errb, Executable: "/opt/agentctl/bin/agentctl",
		Spawner: SpawnerFunc(func(int64, int) error { return errors.New("spawner must not be used without a followup") }),
	}
	for _, m := range mods {
		m(&cfg)
	}
	start := time.Now()
	o := Run(context.Background(), cfg)
	return runResult{o, errb.String(), time.Since(start)}
}

func payload(event string, kv ...string) io.Reader {
	m := map[string]any{"session_id": "sess-1", "cwd": "/w/demo", "hook_event_name": event, "transcript_path": "/nonexistent/t.jsonl"}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return bytes.NewReader(b)
}

func (f *fixture) session() store.Session {
	f.t.Helper()
	s, err := f.st.Sessions().Get(context.Background(), f.sess.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) events(typ string) []store.Event {
	f.t.Helper()
	all, err := f.st.Events().Since(context.Background(), 0, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []store.Event
	for _, e := range all {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (f *fixture) errorLog() string {
	b, _ := os.ReadFile(filepath.Join(f.p.Logs, ErrorLogName))
	return string(b)
}

func decode(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTaskEnvHookAppliesState(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
	r := f.run(f.env("demo"), payload("SessionStart", "source", "startup"))
	if r.Err != nil || r.Orphan != "" || !r.Applied || r.SessionID != f.sess.ID || r.TaskID != "demo" {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	s := f.session()
	if s.State != domain.StateReady || s.Confidence != domain.ConfidenceHook || s.AgentSessionID != "sess-1" || s.LastDetail != "session started (startup)" {
		t.Fatalf("session = %+v", s)
	}
	recv := f.events(domain.EventHookReceived)
	if len(recv) != 1 || recv[0].TaskID != "demo" {
		t.Fatalf("hook.received = %+v", recv)
	}
	if p := decode(t, recv[0].Payload); p["event"] != "SessionStart" || p["source"] != "startup" || p["match"] != "task_env" {
		t.Fatalf("hook.received payload = %v", p)
	}
	if ch := f.events(domain.EventAgentStateChanged); len(ch) != 1 || ch[0].CauseID != recv[0].ID {
		t.Fatalf("agent.state_changed = %+v", ch)
	}
	if f.errorLog() != "" || r.stderr != "" {
		t.Fatalf("unexpected errors: log=%q stderr=%q", f.errorLog(), r.stderr)
	}

	// the full turn: working, awaiting input, working, ready, stopped (sticky)
	for _, step := range []struct {
		event string
		kv    []string
		want  domain.AgentState
	}{
		{"UserPromptSubmit", []string{"prompt", "go"}, domain.StateWorking},
		{"PreToolUse", []string{"tool_name", "AskUserQuestion"}, domain.StateAwaitingInput},
		{"PostToolUse", []string{"tool_name", "AskUserQuestion"}, domain.StateWorking},
		{"Notification", []string{"notification_type", "permission_prompt", "message", "allow Bash?"}, domain.StateAwaitingInput},
		{"PermissionRequest", []string{"tool_name", "Bash"}, domain.StateAwaitingInput},
		{"PostToolUse", []string{"tool_name", "Bash"}, domain.StateWorking},
		{"SessionStart", []string{"source", "compact"}, domain.StateWorking},
		{"Stop", nil, domain.StateReady},
		// SessionEnd leaves the terminal state to reconcile (real exit status)
		{"SessionEnd", []string{"reason", "clear"}, domain.StateReady},
		{"SessionStart", []string{"source", "clear"}, domain.StateReady},
		{"SessionEnd", []string{"reason", "other"}, domain.StateReady},
	} {
		r := f.run(f.env("demo"), payload(step.event, step.kv...))
		if r.Err != nil || r.Orphan != "" {
			t.Fatalf("%s: outcome %+v", step.event, r.Outcome)
		}
		if got := f.session().State; got != step.want {
			t.Fatalf("after %s: state %s, want %s", step.event, got, step.want)
		}
	}
	if s := f.session(); s.LastDetail != "session ended (other)" {
		t.Fatalf("SessionEnd detail = %q", s.LastDetail)
	}
	// terminal states are sticky: a later hook is recorded but changes nothing
	if _, err := f.st.Sessions().Transition(context.Background(), f.sess.ID, domain.StateFailed, domain.ConfidencePane, "exit 3", domain.SourcePane); err != nil {
		t.Fatal(err)
	}
	if r := f.run(f.env("demo"), payload("SessionStart", "source", "startup")); r.Err != nil || r.Applied {
		t.Fatalf("hook on failed = %+v", r.Outcome)
	}
	if s := f.session(); s.State != domain.StateFailed || s.LastDetail != "exit 3" {
		t.Fatalf("failed session revived: %+v", s)
	}
	if f.errorLog() != "" {
		t.Fatalf("a refused transition on a terminal session is not an error: %q", f.errorLog())
	}
}

func TestOrphansChangeNothing(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(env map[string]string, f *fixture)
		reason string
	}{
		{"mismatched TMUX_PANE", func(e map[string]string, _ *fixture) { e["TMUX_PANE"] = "%9" }, "pane_mismatch"},
		{"mismatched server pid", func(e map[string]string, f *fixture) { e["TMUX"] = f.socket + ",4243,0" }, "server_pid_mismatch"},
		{"mismatched socket", func(e map[string]string, f *fixture) { e["TMUX"] = filepath.Join(f.p.Home, "other-sock") + ",4242,0" }, "socket_mismatch"},
		{"no tmux env", func(e map[string]string, _ *fixture) { delete(e, "TMUX"); delete(e, "TMUX_PANE") }, "no_tmux_env"},
		{"unknown task", func(e map[string]string, _ *fixture) { e["AGENTCTL_TASK_ID"] = "ghost" }, "unknown_task"},
		{"invalid task id", func(e map[string]string, _ *fixture) { e["AGENTCTL_TASK_ID"] = "../etc" }, "invalid_task_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
			env := f.env("demo")
			c.mod(env, f)
			r := f.run(env, payload("Stop"))
			if r.Orphan != c.reason || r.Applied || r.Err != nil {
				t.Fatalf("outcome = %+v, want orphan %s", r.Outcome, c.reason)
			}
			if s := f.session(); s.State != domain.StateStarting || s.Confidence != domain.ConfidenceNone || s.AgentSessionID != "" {
				t.Fatalf("orphan changed the session: %+v", s)
			}
			orph := f.events(domain.EventHookOrphan)
			if len(orph) != 1 || len(f.events(domain.EventHookReceived)) != 0 || len(f.events(domain.EventAgentStateChanged)) != 0 {
				t.Fatalf("events: orphan=%d received=%d", len(orph), len(f.events(domain.EventHookReceived)))
			}
			if p := decode(t, orph[0].Payload); p["event"] != "Stop" || p["reason"] != c.reason {
				t.Fatalf("hook.orphan payload = %v", p)
			}
		})
	}
}

func TestAdoptedPaneWithoutTaskEnvUpgradesConfidence(t *testing.T) {
	f := newFixture(t, domain.OriginAdopted, domain.StateUnknown)
	r := f.run(f.env(""), payload("Stop"))
	if r.Err != nil || r.Orphan != "" || !r.Applied || r.SessionID != f.sess.ID {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	if s := f.session(); s.State != domain.StateReady || s.Confidence != domain.ConfidenceHook {
		t.Fatalf("session = %+v", s)
	}
	if p := decode(t, f.events(domain.EventHookReceived)[0].Payload); p["match"] != "pane" {
		t.Fatalf("payload = %v", p)
	}
	// a record-only event does not claim hook confidence for a state it did not set
	g := newFixture(t, domain.OriginAdopted, domain.StateUnknown)
	if r := g.run(g.env(""), payload("SubagentStop")); r.Err != nil || r.Applied {
		t.Fatalf("record-only outcome = %+v", r.Outcome)
	}
	if s := g.session(); s.State != domain.StateUnknown || s.Confidence != domain.ConfidenceNone {
		t.Fatalf("record-only hook upgraded the session: %+v", s)
	}
}

func TestNoTaskEnvMatching(t *testing.T) {
	t.Run("adopted pane on another server is an orphan", func(t *testing.T) {
		f := newFixture(t, domain.OriginAdopted, domain.StateUnknown)
		env := f.env("")
		env["TMUX"] = f.socket + ",999,0"
		if r := f.run(env, payload("Stop")); r.Orphan != "server_pid_mismatch" {
			t.Fatalf("outcome = %+v", r.Outcome)
		}
		if f.session().State != domain.StateUnknown {
			t.Fatal("state changed")
		}
	})
	t.Run("launched session is never matched without its task env", func(t *testing.T) {
		f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
		if r := f.run(f.env(""), payload("Stop")); r.Orphan != "launched_without_task_env" || r.Applied {
			t.Fatalf("outcome = %+v", r.Outcome)
		}
	})
	t.Run("two adopted sessions on one pane are ambiguous", func(t *testing.T) {
		f := newFixture(t, domain.OriginAdopted, domain.StateUnknown)
		f.seed("twin", domain.OriginAdopted, domain.StateUnknown, "%3")
		if r := f.run(f.env(""), payload("Stop")); r.Orphan != "ambiguous_pane" || r.Applied {
			t.Fatalf("outcome = %+v", r.Outcome)
		}
	})
	t.Run("unmanaged pane records nothing", func(t *testing.T) {
		f := newFixture(t, domain.OriginAdopted, domain.StateUnknown)
		env := f.env("")
		env["TMUX_PANE"] = "%12"
		r := f.run(env, payload("Stop"))
		if r.Orphan != "" || r.Applied || r.Err != nil || len(f.events(domain.EventHookOrphan)) != 0 || len(f.events(domain.EventHookReceived)) != 0 {
			t.Fatalf("outcome = %+v", r.Outcome)
		}
	})
	t.Run("not in tmux and no task env is a silent no-op", func(t *testing.T) {
		f := newFixture(t, domain.OriginAdopted, domain.StateUnknown)
		r := f.run(map[string]string{"AGENTCTL_HOME": f.p.Home}, payload("Stop"))
		if r.Orphan != "" || r.Err != nil || f.errorLog() != "" || r.stderr != "" {
			t.Fatalf("outcome = %+v", r.Outcome)
		}
	})
}

func TestBadInputIsLoggedNeverRecorded(t *testing.T) {
	cases := map[string]io.Reader{
		"garbage":     strings.NewReader("\x00\x01 definitely not json"),
		"empty stdin": strings.NewReader(""),
		"over 1 MiB":  io.MultiReader(strings.NewReader(`{"hook_event_name":"Stop","x":"`), strings.NewReader(strings.Repeat("a", MaxPayload)), strings.NewReader(`"}`)),
	}
	for name, stdin := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
			r := f.run(f.env("demo"), stdin)
			if r.Err == nil || r.Applied {
				t.Fatalf("outcome = %+v", r.Outcome)
			}
			log := f.errorLog()
			if strings.Count(log, "\n") != 1 || !strings.Contains(log, "task=demo") {
				t.Fatalf("error log = %q", log)
			}
			if n := len(f.events(domain.EventHookReceived)) + len(f.events(domain.EventHookOrphan)); n != 0 {
				t.Fatalf("%d events recorded for bad input", n)
			}
			assertMode(t, filepath.Join(f.p.Logs, ErrorLogName), 0o600)
		})
	}
}

func TestMissingHomeCreatesNothing(t *testing.T) {
	home := filepath.Join(t.TempDir(), "no-such-home")
	var errb bytes.Buffer
	o := Run(context.Background(), Config{
		Getenv: func(k string) string {
			return map[string]string{"AGENTCTL_HOME": home, "AGENTCTL_TASK_ID": "demo", "TMUX": "/tmp/x,1,0", "TMUX_PANE": "%1"}[k]
		},
		Stdin: payload("Stop"), Stderr: &errb,
	})
	if o.Err == nil || strings.Count(errb.String(), "\n") != 1 || !strings.Contains(errb.String(), "agentctl hook:") {
		t.Fatalf("outcome = %+v stderr = %q", o, errb.String())
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("hook created the missing home: %v", err)
	}
	// no home at all (no AGENTCTL_HOME, XDG_STATE_HOME or HOME) is also fail-open
	if o := Run(context.Background(), Config{Getenv: func(string) string { return "" }, Stdin: payload("Stop"), Stderr: io.Discard}); o.TimedOut {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestMissingRegistryIsNotCreated(t *testing.T) {
	p := paths.ForHome(testenv.TempHome(t))
	env := map[string]string{"AGENTCTL_HOME": p.Home, "AGENTCTL_TASK_ID": "demo", "TMUX": "/tmp/x,1,0", "TMUX_PANE": "%1"}
	o := Run(context.Background(), Config{Getenv: func(k string) string { return env[k] }, Stdin: payload("Stop"), Stderr: io.Discard})
	if o.Err == nil {
		t.Fatalf("outcome = %+v", o)
	}
	if _, err := os.Stat(p.DB); !os.IsNotExist(err) {
		t.Fatalf("hook created the registry: %v", err)
	}
}

// blockingReader never returns: a hook whose stdin is never closed.
type blockingReader struct{ ch chan struct{} }

func (b blockingReader) Read([]byte) (int, error) { <-b.ch; return 0, io.EOF }

func TestBudgetBoundsAStuckHook(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
	// never closed: the abandoned pipeline goroutine stays parked on stdin
	// instead of writing into the temp home while the test cleans it up
	stuck := blockingReader{make(chan struct{})}
	r := f.run(f.env("demo"), stuck, func(c *Config) { c.Budget = 150 * time.Millisecond })
	if !r.TimedOut || r.elapsed > time.Second {
		t.Fatalf("outcome = %+v after %v", r.Outcome, r.elapsed)
	}
	if !strings.Contains(f.errorLog(), "budget") {
		t.Fatalf("error log = %q", f.errorLog())
	}
}

// TestPromptTextNeverStored: only prompt_sha256 reaches the DB, WAL or SHM
// files, and transcript_path (a FIFO here: opening it would block) is never
// opened.
func TestPromptTextNeverStored(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateReady)
	fifo := filepath.Join(f.p.Home, "transcript.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	const canary = "zebra-canary-7f3a91 please refactor the auth module"
	b, _ := json.Marshal(map[string]any{
		"session_id": "s", "hook_event_name": "UserPromptSubmit", "prompt": canary, "transcript_path": fifo, "cwd": "/w",
	})
	r := f.run(f.env("demo"), bytes.NewReader(b))
	if r.Err != nil || !r.Applied || r.TimedOut || r.elapsed > time.Second {
		t.Fatalf("outcome = %+v after %v", r.Outcome, r.elapsed)
	}
	recv := f.events(domain.EventHookReceived)
	if p := decode(t, recv[0].Payload); p["prompt_sha256"] != sha256Hex(canary) {
		t.Fatalf("payload = %v", p)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(f.p.DB + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, needle := range []string{"zebra-canary", "refactor the auth"} {
			if bytes.Contains(data, []byte(needle)) {
				t.Fatalf("%s contains prompt text %q", f.p.DB+suffix, needle)
			}
		}
	}
}

func TestFollowupSeam(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateWorking)
	var calls []string
	var spawned []int64
	seam := []followupEntry{
		{order: 20, name: "trigger", fn: func(ctx context.Context, d Deps, a Applied) error {
			calls = append(calls, "trigger:"+a.Observation.Event+":"+string(a.Session.State))
			if a.Result.Transition.Changed && a.Session.State == domain.StateReady {
				return d.Spawner.SpawnDeliver(a.Session.ID, d.PID)
			}
			return nil
		}},
		{order: 10, name: "ack", fn: func(ctx context.Context, d Deps, a Applied) error {
			calls = append(calls, "ack:"+a.Observation.PromptSHA256)
			panic("followups fail open")
		}},
	}
	r := f.run(f.env("demo"), payload("Stop"), func(c *Config) {
		c.followups = seam
		c.PID = 31337
		c.Spawner = SpawnerFunc(func(id int64, after int) error {
			if after != 31337 {
				t.Errorf("after pid = %d", after)
			}
			spawned = append(spawned, id)
			return nil
		})
	})
	if !r.Applied || strings.Join(calls, ",") != "ack:,trigger:Stop:ready" || len(spawned) != 1 || spawned[0] != f.sess.ID {
		t.Fatalf("outcome %+v calls %v spawned %v", r.Outcome, calls, spawned)
	}
	if !strings.Contains(f.errorLog(), "followup ack") {
		t.Fatalf("followup panic not logged: %q", f.errorLog())
	}
	if f.session().State != domain.StateReady {
		t.Fatal("a failing followup undid the transition")
	}
}

func TestErrorLogRotates(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
	path := filepath.Join(f.p.Logs, ErrorLogName)
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxErrorLog+1), 0o600); err != nil {
		t.Fatal(err)
	}
	f.run(f.env("demo"), strings.NewReader("garbage"))
	if fi, err := os.Stat(path + ".1"); err != nil || fi.Size() != maxErrorLog+1 {
		t.Fatalf("rotated log: %v", err)
	}
	if log := f.errorLog(); strings.Count(log, "\n") != 1 {
		t.Fatalf("new log = %q", log)
	}
}

func TestSettingsSnippetIsValidJSON(t *testing.T) {
	b, err := SettingsSnippet("/opt/agentctl/bin/agentctl")
	if err != nil || !json.Valid(b) || !strings.Contains(string(b), `'/opt/agentctl/bin/agentctl' hook`) {
		t.Fatalf("snippet = %s %v", b, err)
	}
	if _, err := SettingsSnippet("relative/agentctl"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("%s mode %o, want %o", path, fi.Mode().Perm(), want)
	}
}

// TestHookWaitsForALaunchingSession: `agentctl new` reserves the task, starts
// tmux, and inserts the session row only once tmux has returned the pane
// identity. A fast agent's SessionStart hook can arrive in between; it must
// wait for the row instead of becoming an orphan.
func TestHookWaitsForALaunchingSession(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
	ctx := context.Background()
	repo, _ := f.st.Repos().Upsert(ctx, "/repos/main", "main")
	if _, err := f.st.Tasks().Reserve(ctx, store.Task{ID: "late", RepoID: repo.ID, Name: "late", WorktreePath: "/w/late", Branch: "jpark/late"}); err != nil {
		t.Fatal(err)
	}
	env := f.env("late")
	env["TMUX_PANE"] = "%5"
	done := make(chan runResult, 1)
	go func() { done <- f.run(env, payload("SessionStart", "source", "startup")) }()
	time.Sleep(100 * time.Millisecond) // the launch window being simulated, not a wait for a condition
	sess, err := f.st.Sessions().Create(ctx, store.NewSession{
		TaskID: "late", AgentType: "claude", Origin: domain.OriginLaunched, State: domain.StateStarting, Confidence: domain.ConfidenceNone,
		Identity: domain.Identity{Socket: f.socket, ServerPID: serverPID, SessionID: "$1", PaneID: "%5", PanePID: 78},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.Orphan != "" || !r.Applied || r.SessionID != sess.ID {
		t.Fatalf("outcome = %+v", r.Outcome)
	}

	// a task whose session never appears is an orphan once the wait is over
	old := sessionWait
	sessionWait = 50 * time.Millisecond
	defer func() { sessionWait = old }()
	if _, err := f.st.Tasks().Reserve(ctx, store.Task{ID: "never", RepoID: repo.ID, Name: "never", WorktreePath: "/w/never", Branch: "jpark/never"}); err != nil {
		t.Fatal(err)
	}
	if r := f.run(f.env("never"), payload("Stop")); r.Orphan != OrphanNoSession {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
}

// TestRetryImmediateBusy: SQLite can return SQLITE_BUSY from BEGIN IMMEDIATE
// at once (WAL recovery) instead of waiting out busy_timeout; such a fast
// failure is retried within the budget, a slow one (the timeout itself) is not.
func TestRetryImmediateBusy(t *testing.T) {
	busy := errors.New("store: begin: database is locked (5) (SQLITE_BUSY)")
	calls := 0
	_, err := retryFastBusy(context.Background(), func() (store.HookResult, error) {
		calls++
		if calls < 3 {
			return store.HookResult{}, busy
		}
		return store.HookResult{Applied: true}, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls = %d, err = %v", calls, err)
	}
	calls = 0
	_, err = retryFastBusy(context.Background(), func() (store.HookResult, error) {
		calls++
		time.Sleep(fastBusy + 20*time.Millisecond) // waited out a busy timeout
		return store.HookResult{}, busy
	})
	if err == nil || calls != 1 {
		t.Fatalf("slow busy retried: calls = %d", calls)
	}
	calls = 0
	_, err = retryFastBusy(context.Background(), func() (store.HookResult, error) {
		calls++
		return store.HookResult{}, errors.New("store: session 9: not found")
	})
	if err == nil || calls != 1 {
		t.Fatalf("non-busy error retried: calls = %d", calls)
	}
	calls = 0
	if _, err = retryFastBusy(context.Background(), func() (store.HookResult, error) { calls++; return store.HookResult{}, busy }); err == nil || calls != maxBusyRetries+1 {
		t.Fatalf("retries not bounded: calls = %d", calls)
	}
}
