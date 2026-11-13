package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// fakePane is one pane of the fake tmux server (server pid 100).
type fakePane struct {
	tmux.Pane
	output string // what capture-pane returns
}

// fakeTmux answers exactly the invocations the controller makes for the
// manage views: snapshots, has-session, new-session, the identity-guarded
// capture and the identity-guarded stop. Nothing runs; no tmux is needed.
type fakeTmux struct {
	mu    sync.Mutex
	panes []fakePane
	delay time.Duration // every call sleeps this long (slow-tmux tests)
	calls []string      // command names, in order
	next  int
}

func (f *fakeTmux) Run(ctx context.Context, req tmux.Request) (tmux.Result, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return tmux.Result{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range req.Commands {
		f.calls = append(f.calls, c[0])
	}
	cmd := req.Commands[0]
	switch {
	case len(req.Commands) == 2 && cmd[0] == "display-message":
		var b strings.Builder
		b.WriteString("100\n")
		for _, p := range f.panes {
			fmt.Fprintf(&b, "100\t%s\t%d\t%s\t%d\t0\t\t\t%s\t%s\t%s\n", p.SessionID, p.WindowIndex, p.PaneID, p.PanePID,
				p.SessionName, p.CurrentCommand, p.CurrentPath)
		}
		return tmux.Result{Stdout: []byte(b.String())}, nil
	case cmd[0] == "has-session":
		return tmux.Result{}, &tmux.Error{Commands: []string{"has-session"}, Stderr: "can't find session: " + cmd[len(cmd)-1], Err: errors.New("exit status 1")}
	case cmd[0] == "new-session":
		f.next++
		p := fakePane{Pane: tmux.Pane{SessionID: "$" + strconv.Itoa(90+f.next), PaneID: "%" + strconv.Itoa(90+f.next),
			PanePID: 9000 + f.next, SessionName: argAfter(cmd, "-s"), CurrentCommand: "claude", CurrentPath: argAfter(cmd, "-c")}}
		f.panes = append(f.panes, p)
		return tmux.Result{Stdout: []byte(fmt.Sprintf("100\t%s\t%s\t%d\n", p.SessionID, p.PaneID, p.PanePID))}, nil
	case cmd[0] == "if-shell":
		return f.guarded(cmd), nil
	}
	return tmux.Result{}, nil
}

// guarded answers `if-shell -F -t %N <identity condition> <then> <else>`.
func (f *fakeTmux) guarded(cmd []string) tmux.Result {
	target, cond, then := cmd[3], cmd[4], cmd[5]
	i := f.find(target)
	matches := i >= 0 && strings.Contains(cond, "#{pane_pid},"+strconv.Itoa(f.panes[i].PanePID)+"}")
	if !strings.Contains(then, "capture-pane") { // the guarded stop: drop the pane's session
		if matches {
			sess := f.panes[i].SessionID
			var keep []fakePane
			for _, p := range f.panes {
				if p.SessionID != sess {
					keep = append(keep, p)
				}
			}
			f.panes = keep
		}
		return tmux.Result{}
	}
	switch {
	case matches:
		return tmux.Result{Stdout: []byte("agentctl-capture-ok\n" + f.panes[i].output)}
	case i >= 0:
		return tmux.Result{Stdout: []byte("agentctl-capture-mismatch\n")}
	}
	return tmux.Result{Stdout: []byte("agentctl-capture-missing\n")}
}

func (f *fakeTmux) find(paneID string) int {
	for i, p := range f.panes {
		if p.PaneID == paneID {
			return i
		}
	}
	return -1
}

// add registers a live pane.
func (f *fakeTmux) add(p fakePane) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.panes = append(f.panes, p)
}

// setOutput sets what capturing paneID returns.
func (f *fakeTmux) setOutput(paneID, out string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.find(paneID); i >= 0 {
		f.panes[i].output = out
	}
}

// setPID changes a pane's process (a reused %N).
func (f *fakeTmux) setPID(paneID string, pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i := f.find(paneID); i >= 0 {
		f.panes[i].PanePID = pid
	}
}

func (f *fakeTmux) has(paneID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.find(paneID) >= 0
}

func (f *fakeTmux) setDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = d
}

func argAfter(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// manageFixture is the inbox fixture wired to fakeTmux, with an agent
// command that resolves (it never runs: fakeTmux starts nothing).
type manageFixture struct {
	*fixture
	tm *fakeTmux
}

const fakeAgentCmd = `AGENTCTL_AGENT_CMD=["/bin/sh","-c","exec sleep 600","agent"]`

func newManageFixture(t *testing.T) *manageFixture {
	t.Helper()
	return newManageFixtureEnv(t, testenv.NewEnv(t).With(fakeAgentCmd))
}

func newManageFixtureEnv(t *testing.T, env testenv.Env) *manageFixture {
	t.Helper()
	tm := &fakeTmux{}
	f := &fixture{env: env, repo: testenv.TempRepo(t, env), rec: &recRunner{}}
	f.ctl = openController(t, env, tm)
	p, err := paths.Resolve(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	f.st, err = store.Open(context.Background(), p.DB, store.Options{Now: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.Close() })
	return &manageFixture{fixture: f, tm: tm}
}

// seedPanes registers a live fake pane for every seeded session (the
// identities fixture.seed records: pane %<i+1>, pid 4000+i).
func (f *manageFixture) seedPanes(specs []seedSpec) {
	for i, s := range specs {
		if s.state == "" {
			continue
		}
		f.tm.add(fakePane{Pane: tmux.Pane{SessionID: fmt.Sprintf("$%d", i+1), PaneID: fmt.Sprintf("%%%d", i+1),
			PanePID: 4000 + i, SessionName: "jpark-" + s.id, CurrentCommand: "claude", CurrentPath: "/w"}})
	}
}

// newTask launches a real task through the controller: a real git worktree
// beside the temp repo, the session in fakeTmux.
func (f *manageFixture) newTask(t *testing.T, name, goal string) controller.NewTaskResult {
	t.Helper()
	res, err := f.ctl.NewTask(context.Background(), controller.NewTaskRequest{Name: name, Goal: goal, Cwd: f.repo.Root})
	if err != nil {
		t.Fatalf("NewTask %s: %v", name, err)
	}
	return res
}

// writeFile writes rel inside dir.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// changeWorktree commits one file and leaves one modified and one untracked.
func changeWorktree(t *testing.T, f *manageFixture, wt string) {
	t.Helper()
	writeFile(t, wt, "internal/auth/login.go", "package auth\n\nfunc Login() {}\n")
	testenv.GitIn(t, f.env, wt, "add", "-A")
	testenv.GitIn(t, f.env, wt, "commit", "-q", "-m", "login")
	writeFile(t, wt, "README.md", "changed\n")
	writeFile(t, wt, "notes.txt", "one\ntwo\n")
}

// uiActions returns the ui.action payload action names recorded so far.
func uiActions(t *testing.T, ctl *controller.Controller) []string {
	t.Helper()
	evs, err := ctl.EventsSince(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Type != domain.EventUIAction {
			continue
		}
		var p struct {
			Action string `json:"action"`
			TaskID string `json:"task_id"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Action+":"+p.TaskID)
	}
	return out
}

// managed builds a loaded model with a recorded run (so ui.action lands).
func (f *manageFixture) managed(t *testing.T, w, h int, opts Options) Model {
	t.Helper()
	if opts.RunID == "" {
		run, err := f.ctl.UIOpened(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		opts.RunID = run
	}
	return f.model(t, w, h, opts)
}

// pressRun presses a key and runs the commands it produced (and theirs).
func pressRun(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		var cmd tea.Cmd
		m, cmd = pressCmd(t, m, k)
		m = runLong(t, m, cmd)
	}
	return m
}

// longBound is how long a manage-test command may take: these run real git
// (worktree add, diff plumbing) and are slow under a parallel -race run, so
// the inbox helpers' 200ms bound (meant to drop ticks) would drop them. No
// manage flow starts a tick loop (only Init and pollTickMsg do).
const longBound = 20 * time.Second

// runLong is run with longBound and a guard against message loops.
func runLong(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	budget := 500
	var step func(Model, tea.Cmd) Model
	step = func(m Model, cmd tea.Cmd) Model {
		for _, msg := range collectLong(t, cmd) {
			if budget--; budget < 0 {
				t.Fatal("runLong: message loop")
			}
			next, c := m.Update(msg)
			m = step(next.(Model), c)
		}
		return m
	}
	return step(m, cmd)
}

// collectLong is collect with longBound (batches flattened, quits dropped).
func collectLong(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(longBound):
		t.Fatal("a command did not answer within", longBound)
	}
	switch msg := msg.(type) {
	case nil, tea.QuitMsg:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, collectLong(t, c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// typeText types s one rune at a time (as key presses).
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = press(t, m, string(r))
	}
	return m
}

// topView is the view on top (nil when none).
func topView(m Model) view {
	if len(m.views) == 0 {
		return nil
	}
	return m.views[len(m.views)-1]
}
