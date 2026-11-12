package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

type wrapFixture struct {
	*fixture
	codexBin string
	pids     map[string]int // pane id -> pane pid (the "shell" agentctl codex runs under)
}

// newWrapFixture: a controller whose Codex command is `sleep` (any absolute
// executable works: RegisterWrapped only builds the argv).
func newWrapFixture(t *testing.T) *wrapFixture {
	t.Helper()
	f := newFixture(t, sleepAgent)
	// agentctl codex runs inside a pane of this server: $TMUX names it.
	f.env = codexEnv(f.env, []string{"sleep"}).With("TMUX=" + f.srv.Socket + ",1,0")
	f.ctl = openController(t, f.env)
	bin, err := f.ctl.lookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	return &wrapFixture{fixture: f, codexBin: bin, pids: map[string]int{}}
}

// shellPane starts a pane in the repo running a long sleep (standing in for
// the user's shell) and returns its pane id.
func (f *wrapFixture) shellPane(t *testing.T, name string) string {
	t.Helper()
	created, err := f.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: name, Dir: f.repo.Root, Argv: []string{"/bin/sh", "-c", "exec sleep 600"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.pids[created.PaneID] = created.PanePID
	return created.PaneID
}

// in is a WrapRequest made from inside pane (SelfPID = the pane's process).
func (f *wrapFixture) in(pane string, req WrapRequest) WrapRequest {
	req.PaneID, req.SelfPID = pane, f.pids[pane]
	return req
}

func TestRegisterWrappedNewPane(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	pane := f.shellPane(t, "user")
	res, err := f.ctl.RegisterWrapped(context.Background(), f.in(pane, WrapRequest{Name: "w", UserArgs: []string{"--model", "x"}}))
	if err != nil {
		t.Fatal(err)
	}
	flags, _ := codex.HookFlags("/opt/agentctl/bin/agentctl")
	want := append(append([]string{f.codexBin}, flags...), "--model", "x")
	if res.TaskID != "w" || res.Reused || !reflect.DeepEqual(res.Argv, want) {
		t.Fatalf("result = %+v\nargv want %q", res, want)
	}
	home := f.ctl.Paths().Home
	if !slices.Contains(res.Env, "AGENTCTL_TASK_ID=w") || !slices.Contains(res.Env, "AGENTCTL_HOME="+home) {
		t.Fatalf("env = %q", res.Env)
	}
	s := piSession(t, f.ctl, "w")
	if s.ID != res.SessionID || s.Origin != domain.OriginWrapped || s.AgentType != codex.Name ||
		s.State != domain.StateStarting || s.LastDetail != domain.DetailAwaitingFirstPrompt || s.Identity.PaneID != pane {
		t.Fatalf("session = %+v", s)
	}
	if got := eventTypes(t, f.ctl, "w"); !slices.Equal(got, []string{domain.EventTaskCreated, domain.EventSessionWrapped}) {
		t.Fatalf("events = %v", got)
	}
	if row := overviewRow(t, f.ctl, "w"); row.Attention.Section != domain.SectionInitial {
		t.Fatalf("section = %s, want initial", row.Attention.Section)
	}
}

func TestRegisterWrappedDefaultName(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	ctx := context.Background()
	base := domain.SlugFrom(filepath.Base(f.repo.Root)) + "-codex"
	a, err := f.ctl.RegisterWrapped(ctx, f.in(f.shellPane(t, "u1"), WrapRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.ctl.RegisterWrapped(ctx, f.in(f.shellPane(t, "u2"), WrapRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if a.TaskID != base || b.TaskID != base+"-2" {
		t.Fatalf("names = %q %q, want %q %q", a.TaskID, b.TaskID, base, base+"-2")
	}
}

func TestRegisterWrappedRefusesLive(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	pane := f.shellPane(t, "user")
	if _, err := f.ctl.RegisterWrapped(context.Background(), f.in(pane, WrapRequest{Name: "w"})); err != nil {
		t.Fatal(err)
	}
	// the first agentctl codex is still running under the pane
	pid := f.pids[pane]
	f.ctl.procs = func(context.Context) ([]proc, error) {
		return []proc{{PID: pid, PPID: 1, Args: "-zsh"}, {PID: pid + 50, PPID: pid, Args: "/opt/agentctl/bin/agentctl codex --name w"}}, nil
	}
	_, err := f.ctl.RegisterWrapped(context.Background(), f.in(pane, WrapRequest{Name: "w2"}))
	if !errors.Is(err, ErrPaneRegistered) {
		t.Fatalf("second wrap on a live session = %v, want ErrPaneRegistered", err)
	}
	if n := count(t, f.ctl, "tasks"); n != 1 {
		t.Fatalf("tasks = %d", n)
	}
}

func TestRegisterWrappedReusesExitedTask(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	ctx := context.Background()
	pane := f.shellPane(t, "user")
	first, err := f.ctl.RegisterWrapped(ctx, f.in(pane, WrapRequest{Name: "w"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.RecordWrappedExit(ctx, first.SessionID, 0); err != nil {
		t.Fatal(err)
	}
	again, err := f.ctl.RegisterWrapped(ctx, f.in(pane, WrapRequest{Name: "ignored"}))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Reused || again.TaskID != "w" || again.SessionID == first.SessionID {
		t.Fatalf("again = %+v", again)
	}
	if s := piSession(t, f.ctl, "w"); s.ID != again.SessionID || s.State != domain.StateStarting || s.Origin != domain.OriginWrapped {
		t.Fatalf("latest session = %+v", s)
	}
	if n := count(t, f.ctl, "tasks"); n != 1 {
		t.Fatalf("tasks = %d", n)
	}
}

func TestRecordWrappedExit(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	ctx := context.Background()
	ok, _ := f.ctl.RegisterWrapped(ctx, f.in(f.shellPane(t, "u1"), WrapRequest{Name: "ok"}))
	bad, _ := f.ctl.RegisterWrapped(ctx, f.in(f.shellPane(t, "u2"), WrapRequest{Name: "bad"}))
	if err := f.ctl.RecordWrappedExit(ctx, ok.SessionID, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.RecordWrappedExit(ctx, bad.SessionID, 3); err != nil {
		t.Fatal(err)
	}
	if s := piSession(t, f.ctl, "ok"); s.State != domain.StateStopped || s.LastDetail != "codex exited (code 0)" {
		t.Fatalf("ok = %s %q", s.State, s.LastDetail)
	}
	if s := piSession(t, f.ctl, "bad"); s.State != domain.StateFailed || s.LastDetail != "codex exited (code 3)" {
		t.Fatalf("bad = %s %q", s.State, s.LastDetail)
	}
	if ev := piEvents(t, f.ctl, "bad", domain.EventAgentStateChanged); len(ev) == 0 || ev[len(ev)-1]["source"] != domain.SourceWrapper {
		t.Fatalf("state events = %v", ev)
	}
	// already terminal: left alone, not an error
	if err := f.ctl.RecordWrappedExit(ctx, ok.SessionID, 9); err != nil {
		t.Fatal(err)
	}
	if s := piSession(t, f.ctl, "ok"); s.State != domain.StateStopped || s.LastDetail != "codex exited (code 0)" {
		t.Fatalf("terminal session changed: %s %q", s.State, s.LastDetail)
	}
}

func TestRecordWrappedExitRefusesOtherOrigins(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	res, err := f.ctl.NewTask(context.Background(), NewTaskRequest{Name: "l", Goal: "g", Cwd: f.repo.Root})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.RecordWrappedExit(context.Background(), res.SessionID, 0); err == nil {
		t.Fatal("RecordWrappedExit accepted a launched session")
	}
	if s := piSession(t, f.ctl, "l"); s.State != domain.StateStarting {
		t.Fatalf("launched session changed: %s", s.State)
	}
}

func TestRegisterWrappedValidates(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	ctx := context.Background()
	pane := f.shellPane(t, "user")
	for name, req := range map[string]WrapRequest{
		"no pane":      {Name: "a"},
		"bad name":     {Name: "Bad Name", PaneID: pane},
		"nul arg":      {Name: "a", PaneID: pane, UserArgs: []string{"x\x00y"}},
		"missing pane": {Name: "a", PaneID: "%999", SelfPID: f.pids[pane]},
		"no self pid":  {Name: "a", PaneID: pane},
	} {
		if req.PaneID == pane && name != "no self pid" {
			req.SelfPID = f.pids[pane]
		}
		if _, err := f.ctl.RegisterWrapped(ctx, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if n := count(t, f.ctl, "tasks"); n != 0 {
		t.Fatalf("tasks = %d", n)
	}
}

// Review fix: agentctl codex only runs the interactive Codex TUI. A
// non-interactive subcommand has no composer: a paste would sit in the tty
// and run in the shell after Codex exits.
func TestRegisterWrappedRefusesNonInteractiveSubcommands(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	ctx := context.Background()
	pane := f.shellPane(t, "user")
	for _, args := range [][]string{{"exec", "fix it"}, {"e", "x"}, {"review"}, {"login"}, {"mcp", "list"}, {"--help"}, {"-V"}} {
		if _, err := f.ctl.RegisterWrapped(ctx, f.in(pane, WrapRequest{Name: "a", UserArgs: args})); !errors.Is(err, ErrCodexNotInteractive) {
			t.Errorf("%q: err = %v, want ErrCodexNotInteractive", args, err)
		}
	}
	if n := count(t, f.ctl, "tasks"); n != 0 {
		t.Fatalf("tasks = %d", n)
	}
	for i, args := range [][]string{nil, {"fix the tests"}, {"resume", "--last"}, {"fork"}, {"--model", "x", "do it"}} {
		p := f.shellPane(t, "ok"+strconv.Itoa(i))
		if _, err := f.ctl.RegisterWrapped(ctx, f.in(p, WrapRequest{Name: "ok" + strconv.Itoa(i), UserArgs: args})); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}

// Review fix (final #1): agentctl codex must run inside the pane it
// registers. A $TMUX_PANE inherited by a process outside that pane is
// refused.
func TestRegisterWrappedRefusesWhenNotInPane(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	pane := f.shellPane(t, "user")
	_, err := f.ctl.RegisterWrapped(context.Background(), WrapRequest{Name: "w", PaneID: pane, SelfPID: os.Getpid()})
	if !errors.Is(err, ErrNotInPane) {
		t.Fatalf("err = %v, want ErrNotInPane", err)
	}
	if n := count(t, f.ctl, "tasks"); n != 0 {
		t.Fatalf("tasks = %d", n)
	}
}

// Review fix (final #1): the pane's own server ($TMUX) is used; an
// AGENTCTL_TMUX_SOCKET naming another server is refused, not silently used.
func TestRegisterWrappedRefusesOtherSocket(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	pane := f.shellPane(t, "user")
	ctl := openController(t, f.env.With("AGENTCTL_TMUX_SOCKET=/private/tmp/some-other-server"))
	_, err := ctl.RegisterWrapped(context.Background(), f.in(pane, WrapRequest{Name: "w"}))
	if err == nil || !strings.Contains(err.Error(), "AGENTCTL_TMUX_SOCKET") {
		t.Fatalf("err = %v", err)
	}
	ctl2 := openController(t, f.env.Without("TMUX"))
	if _, err := ctl2.RegisterWrapped(context.Background(), f.in(pane, WrapRequest{Name: "w"})); err == nil {
		t.Fatal("registered without $TMUX")
	}
}

// Review fix (final #2): a session whose wrapper was killed (no exit
// recorded) does not block a rerun in the same pane: the stale session is
// ended and a new one starts.
func TestRegisterWrappedRecoversStaleSession(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	ctx := context.Background()
	pane := f.shellPane(t, "user")
	first, err := f.ctl.RegisterWrapped(ctx, f.in(pane, WrapRequest{Name: "w"}))
	if err != nil {
		t.Fatal(err)
	}
	// no RecordWrappedExit: the wrapper died; only this new process runs now
	again, err := f.ctl.RegisterWrapped(ctx, f.in(pane, WrapRequest{}))
	if err != nil {
		t.Fatalf("rerun after a dead wrapper: %v", err)
	}
	if !again.Reused || again.TaskID != "w" || again.SessionID == first.SessionID {
		t.Fatalf("again = %+v", again)
	}
	old, err := f.ctl.store.Sessions().Get(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if old.State != domain.StateStopped || old.LastDetail != DetailWrapperGone {
		t.Fatalf("stale session = %s %q", old.State, old.LastDetail)
	}
}

// Review fix (final #3): a pane that moved to another repo, or whose task
// was closed, gets a new task on rerun instead of being stuck.
func TestRegisterWrappedNewTaskAfterCdOrClose(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	ctx := context.Background()
	other := testenv.TempRepo(t, f.env)
	created, err := f.srv.Client().NewSession(ctx, tmux.NewSessionSpec{Name: "user", Dir: f.repo.Root, Argv: []string{"/bin/sh", "-i"}})
	if err != nil {
		t.Fatal(err)
	}
	pane := created.PaneID
	f.pids[pane] = created.PanePID
	first, err := f.ctl.RegisterWrapped(ctx, f.in(pane, WrapRequest{Name: "w"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.RecordWrappedExit(ctx, first.SessionID, 0); err != nil {
		t.Fatal(err)
	}
	f.srv.Run(t, "send-keys", "-t", pane, "-l", "cd '"+other.Root+"'")
	f.srv.Run(t, "send-keys", "-t", pane, "Enter")
	testenv.WaitFor(t, 0, "pane in the other repo", func() (bool, string) {
		out, _ := f.srv.TryRun("display-message", "-p", "-t", pane, "#{pane_current_path}")
		real, _ := filepath.EvalSymlinks(other.Root)
		return out == real || out == other.Root, out
	})
	moved, err := f.ctl.RegisterWrapped(ctx, f.in(pane, WrapRequest{Name: "w-other"}))
	if err != nil {
		t.Fatalf("rerun after cd: %v", err)
	}
	if moved.Reused || moved.TaskID != "w-other" {
		t.Fatalf("after cd = %+v, want a new task", moved)
	}
	if err := f.ctl.RecordWrappedExit(ctx, moved.SessionID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "w-other"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.ctl.RegisterWrapped(ctx, f.in(pane, WrapRequest{Name: "w3"}))
	if err != nil {
		t.Fatalf("rerun after close: %v", err)
	}
	if fresh.Reused || fresh.TaskID != "w3" {
		t.Fatalf("after close = %+v, want a new task", fresh)
	}
}
