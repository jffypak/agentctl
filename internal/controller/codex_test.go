package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

func codexEnv(env testenv.Env, argv []string) testenv.Env {
	b, _ := json.Marshal(argv)
	return env.With(EnvCodexCmd + "=" + string(b))
}

// codexTrustAgent prints Codex's startup hook-trust screen and waits.
var codexTrustAgent = []string{"sh", "-c",
	"printf '%s\\n' ' Hooks need review' ' › 1. Review hooks' '   2. Trust all and continue' '   3. Continue without trusting (hooks won'\"'\"'t run)'; exec sleep 600",
	"codex"}

func TestAgentFor(t *testing.T) {
	f := newFixture(t, sleepAgent)
	for in, want := range map[string]string{"": claude.Name, "claude": claude.Name, "codex": codex.Name} {
		a, err := f.ctl.agentFor(in)
		if err != nil || a.Name() != want {
			t.Errorf("agentFor(%q) = %v, %v; want %s", in, a, err, want)
		}
	}
	if _, err := f.ctl.agentFor("gemini"); !errors.Is(err, ErrInvalidAgent) {
		t.Errorf("agentFor(gemini) = %v, want ErrInvalidAgent", err)
	}
}

func TestNewTaskCodexUsesCodexArgv(t *testing.T) {
	t.Parallel()
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	argvFile := filepath.Join(t.TempDir(), "argv")
	env := codexEnv(agentEnv(srv.Env, sleepAgent), []string{"sh", "-c", `printf '%s\n' "$@" > "$0"; exec sleep 600`, argvFile})
	repo := testenv.TempRepo(t, env)
	ctl := openController(t, env)
	res, err := ctl.NewTask(context.Background(), NewTaskRequest{Name: "cx", Goal: "do x", Cwd: repo.Root, Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, 0, "argv file", func() (bool, string) {
		b, err := os.ReadFile(argvFile)
		return err == nil && len(b) > 0, string(b)
	})
	b, _ := os.ReadFile(argvFile)
	flags, _ := codex.HookFlags("/opt/agentctl/bin/agentctl")
	if got, want := strings.Split(strings.TrimRight(string(b), "\n"), "\n"), append(flags, "do x"); !reflect.DeepEqual(got, want) {
		t.Fatalf("codex argv = %q\nwant %q", got, want)
	}
	s := piSession(t, ctl, "cx")
	if s.AgentType != codex.Name || s.Origin != domain.OriginLaunched || s.State != domain.StateStarting || res.SessionName != "jpark-cx" {
		t.Fatalf("session = %+v", s)
	}
	if ev := piEvents(t, ctl, "cx", domain.EventSessionStarted); len(ev) != 1 || ev[0]["agent_type"] != "codex" {
		t.Fatalf("session.started = %v", ev)
	}
}

func TestNewTaskCodexNoGoalDetail(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctl := openController(t, codexEnv(f.env, sleepAgent))
	if _, err := ctl.NewTask(context.Background(), NewTaskRequest{Name: "cx", Cwd: f.repo.Root, Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	if s := piSession(t, ctl, "cx"); s.State != domain.StateStarting || s.LastDetail != domain.DetailAwaitingFirstPrompt {
		t.Fatalf("session = %s %q", s.State, s.LastDetail)
	}
	if row := overviewRow(t, ctl, "cx"); row.Attention.Section != domain.SectionInitial {
		t.Fatalf("section = %s, want initial", row.Attention.Section)
	}
}

func TestNewTaskBadAgent(t *testing.T) {
	f := newFixture(t, sleepAgent)
	_, err := f.ctl.NewTask(context.Background(), NewTaskRequest{Name: "x", Goal: "g", Cwd: f.repo.Root, Agent: "gemini"})
	if !errors.Is(err, ErrInvalidAgent) || strings.Contains(err.Error(), "\n") {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, f.ctl, "tasks"); n != 0 {
		t.Fatalf("tasks = %d, want 0", n)
	}
}

func TestNewTaskCodexMissing(t *testing.T) {
	f := newFixture(t, sleepAgent)
	ctl := openController(t, codexEnv(f.env, []string{"no-such-codex-binary"}))
	_, err := ctl.NewTask(context.Background(), NewTaskRequest{Name: "x", Goal: "g", Cwd: f.repo.Root, Agent: "codex"})
	if !errors.Is(err, ErrAgentMissing) || !strings.Contains(err.Error(), EnvCodexCmd) || !strings.Contains(err.Error(), "Codex") {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, ctl, "tasks"); n != 0 {
		t.Fatalf("tasks = %d", n)
	}
}

func newCodexReconcileFixture(t *testing.T, agent []string, goal string) (*Controller, *piClock, *testenv.TmuxServer) {
	t.Helper()
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	env := codexEnv(agentEnv(srv.Env, sleepAgent), agent)
	repo := testenv.TempRepo(t, env)
	clock := &piClock{t: time.Now()}
	ctl := piOpen(t, env, Tunables{StartingTimeout: 30 * time.Second}, clock.Now)
	if _, err := ctl.NewTask(context.Background(), NewTaskRequest{Name: "alpha", Goal: goal, Cwd: repo.Root, Agent: "codex"}); err != nil {
		t.Fatal(err)
	}
	return ctl, clock, srv
}

func TestReconcileCodexHookTrust(t *testing.T) {
	t.Parallel()
	ctl, _, srv := newCodexReconcileFixture(t, codexTrustAgent, "")
	pane := piSession(t, ctl, "alpha").Identity.PaneID
	testenv.WaitFor(t, 0, "trust screen", func() (bool, string) {
		out, _ := srv.TryRun("capture-pane", "-p", "-J", "-t", pane)
		return strings.Contains(out, "Continue without trusting"), out
	})
	piReconcile(t, ctl)
	s := piSession(t, ctl, "alpha")
	if s.State != domain.StateUnknown || s.Confidence != domain.ConfidenceNone || s.LastDetail != domain.HookTrustDetail {
		t.Fatalf("after one poll: %s/%s %q", s.State, s.Confidence, s.LastDetail)
	}
	if row := overviewRow(t, ctl, "alpha"); row.Attention.Section != domain.SectionNeedsYou {
		t.Fatalf("section = %s, want needs_you", row.Attention.Section)
	}
}

// A Claude agent showing Codex's screen is not a Codex trust screen.
func TestReconcileClaudeIgnoresCodexTrustScreen(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t, codexTrustAgent, "")
	f.waitScreen(t, "Continue without trusting")
	piReconcile(t, f.ctl)
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateStarting {
		t.Fatalf("claude session = %s %q, want starting", s.State, s.LastDetail)
	}
}

// Codex without a goal fires no hook until the user types: expected silence,
// never timed out into NEEDS YOU.
func TestReconcileCodexNoGoalNoTimeout(t *testing.T) {
	t.Parallel()
	ctl, clock, _ := newCodexReconcileFixture(t, sleepAgent, "")
	clock.Advance(time.Hour)
	piReconcile(t, ctl)
	s := piSession(t, ctl, "alpha")
	if s.State != domain.StateStarting || s.LastDetail != domain.DetailAwaitingFirstPrompt {
		t.Fatalf("after the timeout: %s %q", s.State, s.LastDetail)
	}
	if row := overviewRow(t, ctl, "alpha"); row.Attention.Section != domain.SectionInitial {
		t.Fatalf("section = %s, want initial", row.Attention.Section)
	}
}

// Codex with a goal should fire hooks at once; silent past the timeout is
// NEEDS YOU with a Codex-specific hint.
func TestReconcileCodexGoalTimesOut(t *testing.T) {
	t.Parallel()
	ctl, clock, _ := newCodexReconcileFixture(t, sleepAgent, "do it")
	clock.Advance(time.Hour)
	piReconcile(t, ctl)
	s := piSession(t, ctl, "alpha")
	if s.State != domain.StateUnknown || !strings.Contains(s.LastDetail, "Codex may be showing its hook-trust screen") {
		t.Fatalf("after the timeout: %s %q", s.State, s.LastDetail)
	}
}

// Claude's timeout detail is unchanged.
func TestReconcileClaudeTimeoutUnchanged(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t, sleepAgent, "")
	f.clock.Advance(time.Hour)
	piReconcile(t, f.ctl)
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateUnknown || !strings.Contains(s.LastDetail, "Claude may be showing its folder-trust or login prompt") {
		t.Fatalf("claude timeout: %s %q", s.State, s.LastDetail)
	}
}
