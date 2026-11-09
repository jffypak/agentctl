package tui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// fixedNow is the clock of every golden: ages are deterministic.
var fixedNow = time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)

func fixedClock() time.Time { return fixedNow }

const testAgentctl = "/opt/agentctl/bin/agentctl"

// recRunner is the TUI tests' tmux: it records every request and answers
// snapshot probes from a canned pane list (server pid 100).
type recRunner struct {
	mu    sync.Mutex
	panes []tmux.Pane
	reqs  []tmux.Request
}

func (r *recRunner) Run(_ context.Context, req tmux.Request) (tmux.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	if len(req.Commands) == 2 && req.Commands[0][0] == "display-message" {
		var b strings.Builder
		b.WriteString("100\n")
		for _, p := range r.panes {
			fmt.Fprintf(&b, "100\t%s\t0\t%s\t%d\t0\t\t\t%s\tclaude\t/w\n", p.SessionID, p.PaneID, p.PanePID, p.SessionName)
		}
		return tmux.Result{Stdout: []byte(b.String())}, nil
	}
	return tmux.Result{}, nil
}

func (r *recRunner) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, req := range r.reqs {
		for _, c := range req.Commands {
			out = append(out, c[0])
		}
	}
	return out
}

type fixture struct {
	env  testenv.Env
	repo *testenv.Repo
	ctl  *controller.Controller
	st   *store.Store // seeding handle (tests may use store; the TUI never does)
	rec  *recRunner
}

// newFixture: a hermetic env, a temp repo named "repo", a controller on a
// temp registry with the fixed clock and the recording tmux runner.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureEnv(t, nil, true)
}

// newFixtureEnv lets a test adjust the environment (e.g. hide tmux) and run
// the controller without the recording runner (so preflight looks for tmux).
func newFixtureEnv(t *testing.T, mod func(testenv.Env) testenv.Env, recording bool) *fixture {
	t.Helper()
	env := testenv.NewEnv(t)
	if mod != nil {
		env = mod(env)
	}
	f := &fixture{env: env, repo: testenv.TempRepo(t, env), rec: &recRunner{}}
	var r tmux.Runner
	if recording {
		r = f.rec
	}
	f.ctl = openController(t, env, r)
	p, err := paths.Resolve(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	f.st, err = store.Open(context.Background(), p.DB, store.Options{Now: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	return f
}

func openController(t *testing.T, env testenv.Env, r tmux.Runner) *controller.Controller {
	t.Helper()
	cfg := controller.Config{Environ: env.Strings(), Now: fixedClock, AgentctlPath: testAgentctl}
	if r != nil {
		cfg.TmuxRunner = r
	}
	ctl, err := controller.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctl.Close() })
	return ctl
}

// seedSpec is one seeded task.
type seedSpec struct {
	id, goal, detail string
	origin           domain.Origin
	state            domain.AgentState // "" = no session
	age              time.Duration
	queued, unconf   int
	closed           bool
}

// everyState covers every agent state, an unconfirmed message, a queued
// message on an unknown agent, a launched agent silent since launch (NEEDS
// YOU), a task without a session yet, and a closed task.
var everyState = []seedSpec{
	{id: "auth-tests", goal: "Write integration tests for the OAuth login flow, including the redirect and the expired-token path", detail: "permission prompt: Bash(npm test -- auth)", origin: domain.OriginLaunched, state: domain.StateAwaitingInput, age: 4 * time.Minute},
	{id: "flaky-ci", goal: "Find why CI flakes on the cache step", detail: "disconnected: pane_missing", origin: domain.OriginLaunched, state: domain.StateDisconnected, age: 12 * time.Minute},
	{id: "migrate-db", goal: "Add the orders migration", detail: "exit 1", origin: domain.OriginLaunched, state: domain.StateFailed, age: 9 * time.Minute},
	{id: "api-docs", goal: "Regenerate the API reference", detail: "turn finished", origin: domain.OriginLaunched, state: domain.StateReady, age: 2 * time.Minute, unconf: 1},
	{id: "legacy-pane", goal: "", detail: "", origin: domain.OriginAdopted, state: domain.StateUnknown, age: time.Hour, queued: 1},
	{id: "silent-agent", goal: "Rename the config package", detail: "no hook within 30s — Claude may be showing its folder-trust or login prompt; attach to check", origin: domain.OriginLaunched, state: domain.StateUnknown, age: 50 * time.Second},
	{id: "docs-site", goal: "Fix broken links in the docs site", detail: "turn finished", origin: domain.OriginLaunched, state: domain.StateReady, age: 30 * time.Second},
	{id: "old-spike", goal: "Spike: try the new router", detail: "exit 0", origin: domain.OriginLaunched, state: domain.StateStopped, age: 3 * time.Hour},
	{id: "payments", goal: "Refactor payment retries", detail: "Bash: go test ./...", origin: domain.OriginLaunched, state: domain.StateWorking, age: 45 * time.Second},
	{id: "fresh-task", goal: "Add a health endpoint", origin: domain.OriginLaunched, state: domain.StateStarting, age: 5 * time.Second},
	{id: "adopted-shell", goal: "", origin: domain.OriginAdopted, state: domain.StateUnknown, age: 49 * time.Hour},
	{id: "reserving", goal: "Being created right now", age: time.Second},
	{id: "archived", goal: "Old work", detail: "turn finished", origin: domain.OriginLaunched, state: domain.StateReady, age: 72 * time.Hour, closed: true},
}

// seed writes specs into the fixture repo's registry.
func (f *fixture) seed(t *testing.T, specs []seedSpec) {
	t.Helper()
	ctx := context.Background()
	repo, err := f.st.Repos().Upsert(ctx, f.repo.Root, "repo")
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range specs {
		if _, err := f.st.Tasks().Reserve(ctx, store.Task{ID: s.id, RepoID: repo.ID, Name: s.id, Goal: s.goal,
			WorktreePath: "/src/repo-worktrees/" + s.id, Branch: "agentctl/" + s.id, BaseRef: "main",
			BaseSHA: "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"}); err != nil {
			t.Fatal(err)
		}
		since := fixedNow.Add(-s.age)
		f.exec(t, `UPDATE tasks SET created_at = ? WHERE id = ?`, since.UnixMilli(), s.id)
		if s.closed {
			f.exec(t, `UPDATE tasks SET state = 'closed', closed_at = ? WHERE id = ?`, fixedNow.UnixMilli(), s.id)
		}
		if s.state == "" {
			continue
		}
		initial := domain.StateStarting
		if s.origin == domain.OriginAdopted {
			initial = domain.StateUnknown
		}
		sess, err := f.st.Sessions().Create(ctx, store.NewSession{TaskID: s.id, AgentType: "claude", Origin: s.origin,
			Identity:    domain.Identity{Socket: "/tmp/tmux-501/default", ServerPID: 100, SessionID: fmt.Sprintf("$%d", i+1), PaneID: fmt.Sprintf("%%%d", i+1), PanePID: 4000 + i},
			SessionName: "agentctl-" + s.id, State: initial, Confidence: domain.ConfidenceNone})
		if err != nil {
			t.Fatal(err)
		}
		if s.state != initial {
			conf := domain.ConfidenceHook
			switch {
			case s.state.IsTerminal():
				conf = domain.ConfidencePane
			case s.state == domain.StateUnknown: // a launched agent that never sent a hook (starting timeout)
				conf = domain.ConfidenceNone
			}
			if _, err := f.st.Sessions().Transition(ctx, sess.ID, s.state, conf, s.detail, domain.SourceHook); err != nil {
				t.Fatalf("%s -> %s: %v", s.id, s.state, err)
			}
		}
		f.exec(t, `UPDATE sessions SET state_since = ? WHERE id = ?`, since.UnixMilli(), sess.ID)
		for range s.queued {
			f.message(t, s.id, sess.ID, "queued")
		}
		for range s.unconf {
			f.message(t, s.id, sess.ID, "unconfirmed")
		}
	}
}

func (f *fixture) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.st.DB().Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) message(t *testing.T, task string, sess int64, state string) {
	f.exec(t, `INSERT INTO messages (task_id, session_id, body, body_sha256, state, created_at) VALUES (?, ?, 'run the tests', 'x', ?, ?)`,
		task, sess, state, fixedNow.UnixMilli())
}

// model builds a loaded model of the fixture at w x h (no program running:
// commands are executed by the test when it wants them).
func (f *fixture) model(t *testing.T, w, h int, opts Options) Model {
	t.Helper()
	opts.Controller = f.ctl
	if opts.Cwd == "" {
		opts.Cwd = f.repo.Root
	}
	if opts.Binding == "" {
		opts.Binding = "bind-key a display-popup -E -w 80% -h 75% -d '#{pane_current_path}' '" + testAgentctl + "'"
	}
	m := New(context.Background(), opts)
	m = update(t, m, tea.WindowSizeMsg{Width: w, Height: h})
	return update(t, m, loadCmd(m.sh)())
}

// update applies one message and returns the model (commands are dropped).
func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

// press applies key presses (by tea key string: "j", "enter", "esc", "/").
func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m, _ = pressCmd(t, m, k)
	}
	return m
}

// pressCmd applies one key and returns the command it produced.
func pressCmd(t *testing.T, m Model, k string) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(keyMsg(k))
	return next.(Model), cmd
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// run executes cmd (and nested batches) synchronously, feeding results back,
// skipping commands that do not answer within a short bound (ticks).
func run(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range collect(cmd) {
		var next tea.Cmd
		var nm tea.Model
		nm, next = m.Update(msg)
		m = nm.(Model)
		m = run(t, m, next)
	}
	return m
}

const cmdBound = 200 * time.Millisecond

// collect runs cmd and returns its messages, flattening tea.BatchMsg and
// dropping commands that block (ticks), quits and execs.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(cmdBound):
		return nil
	}
	switch msg := msg.(type) {
	case nil, tea.QuitMsg:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, collect(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// isQuit reports whether cmd (possibly a batch) quits.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if _, ok := msg.(tea.QuitMsg); ok {
			return true
		}
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				if isQuit(c) {
					return true
				}
			}
		}
	case <-time.After(cmdBound):
	}
	return false
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;:?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// plain strips escape sequences.
func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

// screen is the plain text of the current view, one string per line,
// trailing spaces trimmed.
func screen(m Model) []string {
	lines := strings.Split(plain(m.View().Content), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return lines
}

func screenText(m Model) string { return strings.Join(screen(m), "\n") }

// lineWith returns the first raw (styled) line whose plain text contains s.
func lineWith(m Model, s string) string {
	for _, l := range strings.Split(m.View().Content, "\n") {
		if strings.Contains(plain(l), s) {
			return l
		}
	}
	return ""
}
