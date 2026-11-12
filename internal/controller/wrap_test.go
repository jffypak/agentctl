package controller

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/tmux"
)

type wrapFixture struct {
	*fixture
	codexBin string
}

// newWrapFixture: a controller whose Codex command is `sleep` (any absolute
// executable works: RegisterWrapped only builds the argv).
func newWrapFixture(t *testing.T) *wrapFixture {
	t.Helper()
	f := newFixture(t, sleepAgent)
	f.env = codexEnv(f.env, []string{"sleep"})
	f.ctl = openController(t, f.env)
	bin, err := f.ctl.lookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	return &wrapFixture{fixture: f, codexBin: bin}
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
	return created.PaneID
}

func TestRegisterWrappedNewPane(t *testing.T) {
	t.Parallel()
	f := newWrapFixture(t)
	pane := f.shellPane(t, "user")
	res, err := f.ctl.RegisterWrapped(context.Background(), WrapRequest{Name: "w", PaneID: pane, UserArgs: []string{"--model", "x"}})
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
	a, err := f.ctl.RegisterWrapped(ctx, WrapRequest{PaneID: f.shellPane(t, "u1")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.ctl.RegisterWrapped(ctx, WrapRequest{PaneID: f.shellPane(t, "u2")})
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
	if _, err := f.ctl.RegisterWrapped(context.Background(), WrapRequest{Name: "w", PaneID: pane}); err != nil {
		t.Fatal(err)
	}
	_, err := f.ctl.RegisterWrapped(context.Background(), WrapRequest{Name: "w2", PaneID: pane})
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
	first, err := f.ctl.RegisterWrapped(ctx, WrapRequest{Name: "w", PaneID: pane})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ctl.RecordWrappedExit(ctx, first.SessionID, 0); err != nil {
		t.Fatal(err)
	}
	again, err := f.ctl.RegisterWrapped(ctx, WrapRequest{Name: "ignored", PaneID: pane})
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
	ok, _ := f.ctl.RegisterWrapped(ctx, WrapRequest{Name: "ok", PaneID: f.shellPane(t, "u1")})
	bad, _ := f.ctl.RegisterWrapped(ctx, WrapRequest{Name: "bad", PaneID: f.shellPane(t, "u2")})
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
		"missing pane": {Name: "a", PaneID: "%999"},
	} {
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
		if _, err := f.ctl.RegisterWrapped(ctx, WrapRequest{Name: "a", PaneID: pane, UserArgs: args}); !errors.Is(err, ErrCodexNotInteractive) {
			t.Errorf("%q: err = %v, want ErrCodexNotInteractive", args, err)
		}
	}
	if n := count(t, f.ctl, "tasks"); n != 0 {
		t.Fatalf("tasks = %d", n)
	}
	for i, args := range [][]string{nil, {"fix the tests"}, {"resume", "--last"}, {"fork"}, {"--model", "x", "do it"}} {
		p := f.shellPane(t, "ok"+strconv.Itoa(i))
		if _, err := f.ctl.RegisterWrapped(ctx, WrapRequest{Name: "ok" + strconv.Itoa(i), PaneID: p, UserArgs: args}); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
}
