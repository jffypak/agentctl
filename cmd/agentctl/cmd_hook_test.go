package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
)

// hookBudgetLimit is the acceptance bound on one `agentctl hook` process.
const hookBudgetLimit = 2 * time.Second

func TestHooksSnippetMatchesGeneratedSettings(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	p, _ := paths.Resolve(f.env.Getenv)
	file, err := os.ReadFile(p.ClaudeSettings)
	if err != nil {
		t.Fatalf("new did not generate the hook settings: %v", err)
	}
	r := runCmd(t, f.env, t.TempDir(), "hooks-snippet")
	if r.code != 0 || !json.Valid([]byte(r.stdout)) {
		t.Fatalf("hooks-snippet = %+v", r)
	}
	var snippet, generated map[string]any
	_ = json.Unmarshal([]byte(r.stdout), &snippet)
	_ = json.Unmarshal(file, &generated)
	if !reflect.DeepEqual(snippet["hooks"], generated["hooks"]) || snippet["hooks"] == nil {
		t.Fatalf("snippet hooks differ from %s:\n%s\nvs\n%s", p.ClaudeSettings, r.stdout, file)
	}
	if r.stdout != string(file) {
		t.Fatal("snippet is not byte-identical to the generated file")
	}
	if !strings.Contains(r.stdout, `"'/opt/agentctl/bin/agentctl' hook"`) {
		t.Fatalf("snippet command: %s", r.stdout)
	}
	if r := runCmd(t, f.env, t.TempDir(), "hooks-snippet", "extra"); r.code != 2 || r.stdout != "" {
		t.Fatalf("hooks-snippet extra = %+v", r)
	}
}

// TestHookInProcessNeverWritesStdout runs the subcommand through run() with
// arguments a confused caller might pass; stdout must stay empty and exit 0.
func TestHookInProcessNeverWritesStdout(t *testing.T) {
	env := testenv.NewEnv(t).With("AGENTCTL_TASK_ID=demo", "TMUX=/tmp/x,1,0", "TMUX_PANE=%1")
	for _, args := range [][]string{{"hook"}, {"hook", "--help"}, {"hook", "-h", "x"}} {
		var out, errb bytes.Buffer
		sys := &system{stdin: strings.NewReader("not json"), stdout: &out, stderr: &errb,
			environ: env.Strings(), cwd: t.TempDir(), now: time.Now, executable: "/opt/agentctl/bin/agentctl"}
		if code := run(context.Background(), args, sys); code != 0 || out.Len() != 0 {
			t.Fatalf("%v: code %d stdout %q", args, code, out.String())
		}
	}
}

// hookFixture is a registry with task demo bound to pane %3 of a (fake)
// server pid 4242 on socket <home>/tmux-sock. The hook only compares that
// identity with $TMUX/$TMUX_PANE, so no tmux server is needed.
type hookFixture struct {
	env  testenv.Env
	p    paths.Paths
	sess store.Session
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	base := testenv.NewEnv(t)
	p := paths.ForHome(base.Get("AGENTCTL_HOME"))
	if err := paths.EnsureHome(p); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(p.Home, "tmux-sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo, _ := st.Repos().Upsert(ctx, "/repos/main", "main")
	if _, err := st.Tasks().Reserve(ctx, store.Task{ID: "demo", RepoID: repo.ID, Name: "demo", WorktreePath: "/w", Branch: "agentctl/demo"}); err != nil {
		t.Fatal(err)
	}
	sess, err := st.Sessions().Create(ctx, store.NewSession{
		TaskID: "demo", AgentType: "claude", Origin: domain.OriginLaunched, State: domain.StateStarting, Confidence: domain.ConfidenceNone,
		Identity: domain.Identity{Socket: sock, ServerPID: 4242, SessionID: "$0", PaneID: "%3", PanePID: 77},
	})
	if err != nil {
		t.Fatal(err)
	}
	env := base.With("AGENTCTL_TASK_ID=demo", "TMUX="+sock+",4242,0", "TMUX_PANE=%3")
	return &hookFixture{env: env, p: p, sess: sess}
}

func (h *hookFixture) state(t *testing.T) domain.AgentState {
	t.Helper()
	st, err := store.Open(context.Background(), h.p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := st.Sessions().Get(context.Background(), h.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s.State
}

func (h *hookFixture) errorLog() string {
	b, _ := os.ReadFile(filepath.Join(h.p.Logs, "hook-errors.log"))
	return string(b)
}

type hookRun struct {
	code           int
	stdout, stderr string
	elapsed        time.Duration
}

// runHookProcess runs the built agentctl as Claude Code would: payload on
// stdin, the pane environment, and a measured wall-clock time.
func runHookProcess(t *testing.T, bin string, env testenv.Env, stdin io.Reader) hookRun {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command(bin, "hook")
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Dir = env.Strings(), stdin, &out, &errb, t.TempDir()
	start := time.Now()
	err := cmd.Run()
	r := hookRun{code: cmd.ProcessState.ExitCode(), stdout: out.String(), stderr: errb.String(), elapsed: time.Since(start)}
	if err != nil && r.code == 0 {
		t.Fatalf("run hook: %v", err)
	}
	return r
}

func hookPayload(event string, kv ...string) io.Reader {
	m := map[string]any{"session_id": "s-1", "cwd": "/w", "hook_event_name": event, "transcript_path": "/nonexistent.jsonl"}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return bytes.NewReader(b)
}

// TestHookProcessFailsOpenWithinBudget is the invariant-7 acceptance test:
// for every input the real binary exits 0, prints nothing on stdout, and
// finishes in under 2 s (measured), logging errors to logs/hook-errors.log.
func TestHookProcessFailsOpenWithinBudget(t *testing.T) {
	t.Parallel()
	bin := testenv.BuildAgentctl(t)
	// The first exec of a freshly built binary pays macOS's one-time code
	// scan (~250 ms); an installed agentctl does not. Warm it once (no task
	// env, no tmux: an unmanaged no-op) so the budget measures the hook.
	warm := exec.Command(bin, "hook")
	warm.Env = testenv.NewEnv(t).Strings() // hermetic: no TMUX/TMUX_PANE, private home
	if out, err := warm.CombinedOutput(); err != nil || len(out) != 0 {
		t.Fatalf("warm-up hook: %v %q", err, out)
	}
	huge := func() io.Reader {
		return io.MultiReader(strings.NewReader(`{"hook_event_name":"Stop","pad":"`), strings.NewReader(strings.Repeat("a", 1<<20)), strings.NewReader(`"}`))
	}
	cases := []struct {
		name      string
		stdin     func() io.Reader
		env       func(h *hookFixture) testenv.Env
		hold      bool // hold the DB write lock from this (another) process
		wantState domain.AgentState
		wantLog   string // substring expected in logs/hook-errors.log ("" = log stays empty)
		wantErr   string // substring expected on stderr instead (missing home)
	}{
		{name: "valid payload", stdin: func() io.Reader { return hookPayload("Stop") }, wantState: domain.StateReady},
		{name: "garbage", stdin: func() io.Reader { return strings.NewReader("\x1b[2J garbage {") }, wantState: domain.StateStarting, wantLog: "not a JSON object"},
		{name: "empty stdin", stdin: func() io.Reader { return strings.NewReader("") }, wantState: domain.StateStarting, wantLog: "empty hook payload"},
		{name: "over 1 MiB", stdin: huge, wantState: domain.StateStarting, wantLog: "exceeds"},
		{name: "missing home", stdin: func() io.Reader { return hookPayload("Stop") },
			env:       func(h *hookFixture) testenv.Env { return h.env.With("AGENTCTL_HOME=" + filepath.Join(h.p.Home, "missing")) },
			wantState: domain.StateStarting, wantErr: "does not exist"},
		{name: "DB write-locked by another process", stdin: func() io.Reader { return hookPayload("Stop") }, hold: true,
			wantState: domain.StateStarting, wantLog: "task=demo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := newHookFixture(t)
			env := h.env
			if c.env != nil {
				env = c.env(h)
			}
			var release func()
			if c.hold {
				release = holdWriteLock(t, h.p.DB)
			}
			r := runHookProcess(t, bin, env, c.stdin())
			if release != nil {
				release()
			}
			t.Logf("%s: exit %d in %v", c.name, r.code, r.elapsed)
			if r.code != 0 || r.stdout != "" || r.elapsed >= hookBudgetLimit {
				t.Fatalf("exit %d, stdout %q, elapsed %v (stderr %q)", r.code, r.stdout, r.elapsed, r.stderr)
			}
			if got := h.state(t); got != c.wantState {
				t.Fatalf("state = %s, want %s", got, c.wantState)
			}
			log := h.errorLog()
			switch {
			case c.wantLog == "" && log != "":
				t.Fatalf("unexpected error log: %q", log)
			case c.wantLog != "" && (!strings.Contains(log, c.wantLog) || strings.Count(log, "\n") != 1):
				t.Fatalf("error log = %q, want one line containing %q", log, c.wantLog)
			}
			if c.wantErr != "" && !strings.Contains(r.stderr, c.wantErr) {
				t.Fatalf("stderr = %q, want %q", r.stderr, c.wantErr)
			}
			if c.wantErr != "" {
				if _, err := os.Stat(filepath.Join(h.p.Home, "missing")); !os.IsNotExist(err) {
					t.Fatal("the hook created the missing home")
				}
			}
		})
	}
}

// holdWriteLock takes the registry's write lock (BEGIN IMMEDIATE + a write)
// in this test process — a different process from the hook — until released.
func holdWriteLock(t *testing.T, db string) func() {
	t.Helper()
	st, err := store.Open(context.Background(), db, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.DB().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET last_detail = 'locked'`); err != nil {
		t.Fatal(err)
	}
	return func() {
		_ = tx.Rollback()
		_ = st.Close()
	}
}
