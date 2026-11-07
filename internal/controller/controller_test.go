package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

var sleepAgent = []string{"sh", "-c", "exec sleep 600", "agent"}

type fixture struct {
	env  testenv.Env
	srv  *testenv.TmuxServer
	repo *testenv.Repo
	ctl  *Controller
}

func agentEnv(env testenv.Env, argv []string) testenv.Env {
	b, _ := json.Marshal(argv)
	return env.With(EnvAgentCmd + "=" + string(b))
}

func newFixture(t *testing.T, agent []string) *fixture {
	t.Helper()
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	env := agentEnv(srv.Env, agent)
	f := &fixture{env: env, srv: srv, repo: testenv.TempRepo(t, env)}
	f.ctl = openController(t, env)
	return f
}

func openController(t *testing.T, env testenv.Env) *Controller {
	t.Helper()
	c, err := Open(context.Background(), Config{Environ: env.Strings(), AgentctlPath: "/opt/agentctl/bin/agentctl"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func count(t *testing.T, c *Controller, table string) int {
	t.Helper()
	var n int
	if err := c.store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func eventTypes(t *testing.T, c *Controller, task string) []string {
	t.Helper()
	evs, err := c.store.Events().ForTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// TestNewTaskLaunchesWorktreeAndSession is the walking-skeleton integration
// acceptance: worktree on agentctl/demo from HEAD, session agentctl-demo with
// cwd in the worktree, env vars present, remain-on-exit on, stored identity
// equal to list-panes/display-message, state starting, events task.created +
// session.started.
func TestNewTaskLaunchesWorktreeAndSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	head := f.repo.HeadSHA(t)

	res, err := f.ctl.NewTask(ctx, NewTaskRequest{Name: "demo", Goal: "write tests", Cwd: f.repo.Root})
	if err != nil {
		t.Fatal(err)
	}
	wantWT := filepath.Join(f.repo.WorktreesDir(), "demo")
	if res.Worktree != wantWT || res.Branch != "agentctl/demo" || res.BaseSHA != head || res.BaseRef != "main" || res.State != domain.StateStarting {
		t.Fatalf("result = %+v", res)
	}
	if got := testenv.GitIn(t, f.env, wantWT, "rev-parse", "--abbrev-ref", "HEAD"); got != "agentctl/demo" {
		t.Fatalf("worktree branch = %q", got)
	}
	if got := testenv.GitIn(t, f.env, wantWT, "rev-parse", "HEAD"); got != head {
		t.Fatalf("worktree HEAD = %q, want %q", got, head)
	}

	sess, err := f.ctl.store.Sessions().LatestForTask(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if sess.State != domain.StateStarting || sess.Confidence != domain.ConfidenceNone || sess.Origin != domain.OriginLaunched ||
		sess.AgentType != "claude" || sess.SessionName != "agentctl-demo" {
		t.Fatalf("session = %+v", sess)
	}
	snap := f.srv.Snapshot(t)
	pane, ok := snap.Find(sess.Identity.PaneID)
	if !ok {
		t.Fatalf("stored pane %s not in list-panes %+v", sess.Identity.PaneID, snap.Panes)
	}
	if sess.Identity.Socket != f.srv.Socket || sess.Identity.ServerPID != snap.ServerPID ||
		sess.Identity.SessionID != pane.SessionID || sess.Identity.PanePID != pane.PanePID || pane.SessionName != "agentctl-demo" {
		t.Fatalf("stored identity %+v != list-panes %+v (server %d, socket %s)", sess.Identity, pane, snap.ServerPID, f.srv.Socket)
	}
	disp := f.srv.Run(t, "display-message", "-p", "-t", sess.Identity.PaneID, "#{pid} #{session_id} #{pane_id} #{pane_pid} #{socket_path}")
	want := strings.Join([]string{strconv.Itoa(sess.Identity.ServerPID), sess.Identity.SessionID, sess.Identity.PaneID,
		strconv.Itoa(sess.Identity.PanePID)}, " ")
	if !strings.HasPrefix(disp, want+" ") || !tmux.SameSocket(strings.TrimPrefix(disp, want+" "), sess.Identity.Socket) {
		t.Fatalf("display-message = %q, want %q + socket", disp, want)
	}
	if pane.CurrentPath != wantWT {
		t.Fatalf("pane cwd = %q, want %q", pane.CurrentPath, wantWT)
	}
	for k, v := range map[string]string{"AGENTCTL_TASK_ID": "demo", "AGENTCTL_HOME": f.ctl.paths.Home} {
		if got := f.srv.Run(t, "show-environment", "-t", sess.Identity.SessionID, k); got != k+"="+v {
			t.Fatalf("show-environment %s = %q", k, got)
		}
	}
	if got := f.srv.Run(t, "show-options", "-w", "-v", "-t", sess.Identity.SessionID+":", "remain-on-exit"); got != "on" {
		t.Fatalf("remain-on-exit = %q", got)
	}
	if got := eventTypes(t, f.ctl, "demo"); strings.Join(got, ",") != "task.created,session.started" {
		t.Fatalf("events = %v", got)
	}
	l, p := f.ctl.liveSession(ctx, sess)
	if !l.IsAlive() || p.PaneID != sess.Identity.PaneID {
		t.Fatalf("liveSession = %v %+v", l, p)
	}
}

func readNULArgs(t *testing.T, path string) []string {
	t.Helper()
	var data []byte
	testenv.WaitFor(t, 0, "agent argv file", func() (bool, string) {
		b, err := os.ReadFile(path)
		data = b
		return err == nil, ""
	})
	parts := strings.Split(string(data), "\x00")
	return parts[:len(parts)-1]
}

func TestNewTaskGoalIsOneArgvElementAndNeverShellParsed(t *testing.T) {
	t.Parallel()
	argvFile := filepath.Join(t.TempDir(), "argv")
	recorder := []string{"sh", "-c", `for a in "$@"; do printf '%s\0' "$a"; done > "$0.tmp" && mv "$0.tmp" "$0"; exec sleep 600`, argvFile}
	f := newFixture(t, recorder)
	goal := `$(touch pwned) "quoted"`
	if _, err := f.ctl.NewTask(context.Background(), NewTaskRequest{Name: "inj", Goal: goal, Cwd: f.repo.Root}); err != nil {
		t.Fatal(err)
	}
	got := readNULArgs(t, argvFile)
	want := []string{"--settings", filepath.Join(f.ctl.paths.Home, "hooks", "claude-settings.json"), goal}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("agent argv = %q, want %q", got, want)
	}
	for _, root := range []string{filepath.Dir(f.repo.Root), f.env.Get("HOME"), f.ctl.paths.Home} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Name() == "pwned" {
				t.Errorf("goal was shell-evaluated: %s exists", p)
			}
			return nil
		})
	}
}

func TestNewTaskAgentExit3LeavesDeadPane(t *testing.T) {
	t.Parallel()
	f := newFixture(t, []string{"sh", "-c", "exit 3", "agent"})
	ctx := context.Background()
	if _, err := f.ctl.NewTask(ctx, NewTaskRequest{Name: "crash", Goal: "x", Cwd: f.repo.Root}); err != nil {
		t.Fatal(err)
	}
	sess, _ := f.ctl.store.Sessions().LatestForTask(ctx, "crash")
	var p tmux.Pane
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		p, _ = f.srv.Snapshot(t).Find(sess.Identity.PaneID)
		return p.Dead, ""
	})
	if p.DeadStatus != 3 {
		t.Fatalf("pane_dead_status = %d, want 3", p.DeadStatus)
	}
	l, lp := f.ctl.liveSession(ctx, sess)
	st, detail, ok := l.ImpliedState()
	if l.Status != domain.LivenessDead || l.ExitStatus != 3 || !ok || st != domain.StateFailed || detail != "exit 3" || lp.PaneID != sess.Identity.PaneID {
		t.Fatalf("liveSession = %+v (implied %s %q), pane %+v", l, st, detail, lp)
	}
}

func TestNewTaskInsideWorktreeRegistersAgainstMainRepo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	first, err := f.ctl.NewTask(ctx, NewTaskRequest{Name: "one", Goal: "a", Cwd: f.repo.Root})
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(first.Worktree, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := f.ctl.NewTask(ctx, NewTaskRequest{Name: "two", Goal: "b", Cwd: sub})
	if err != nil {
		t.Fatal(err)
	}
	if second.RepoRoot != f.repo.Root || second.Worktree != filepath.Join(f.repo.WorktreesDir(), "two") {
		t.Fatalf("second = %+v", second)
	}
	if n := count(t, f.ctl, "repos"); n != 1 {
		t.Fatalf("repos = %d, want 1", n)
	}
	t1, _ := f.ctl.store.Tasks().Get(ctx, "one")
	t2, _ := f.ctl.store.Tasks().Get(ctx, "two")
	if t1.RepoID != t2.RepoID {
		t.Fatalf("repo ids differ: %d vs %d", t1.RepoID, t2.RepoID)
	}
	ov, err := f.ctl.Overview(ctx, OverviewQuery{Cwd: first.Worktree})
	if err != nil || ov.AllRepos || ov.RepoRoot != f.repo.Root || len(ov.Rows) != 2 {
		t.Fatalf("overview from inside a worktree = %+v %v", ov, err)
	}
}

func TestNewTaskConcurrentSameNameExactlyOneWins(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	other := openController(t, f.env) // a second "process": its own store pool
	ctx := context.Background()
	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		errs  = make([]error, 2)
	)
	for i, c := range []*Controller{f.ctl, other} {
		wg.Add(1)
		go func(i int, c *Controller) {
			defer wg.Done()
			<-start
			_, errs[i] = c.NewTask(ctx, NewTaskRequest{Name: "x", Goal: "race", Cwd: f.repo.Root})
		}(i, c)
	}
	close(start)
	wg.Wait()
	ok, failed := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrTaskExists), errors.Is(err, ErrBranchExists), errors.Is(err, ErrWorktreeExists), errors.Is(err, ErrSessionExists):
			failed++
		default:
			t.Fatalf("unexpected error kind: %v", err)
		}
	}
	if ok != 1 || failed != 1 {
		t.Fatalf("ok=%d failed=%d errs=%v, want exactly one winner", ok, failed, errs)
	}
	wts := strings.Count(f.repo.Git(t, "worktree", "list", "--porcelain"), "worktree ")
	if wts != 2 {
		t.Fatalf("worktrees = %d, want main + 1", wts)
	}
	if count(t, f.ctl, "tasks") != 1 || count(t, f.ctl, "sessions") != 1 {
		t.Fatalf("tasks=%d sessions=%d", count(t, f.ctl, "tasks"), count(t, f.ctl, "sessions"))
	}
	n := 0
	for _, p := range f.srv.Snapshot(t).Panes {
		if p.SessionName == "agentctl-x" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("agentctl-x panes = %d, want 1", n)
	}
}

func TestNewTaskTmuxFailureAfterWorktreeKeepsTask(t *testing.T) {
	t.Parallel()
	base := testenv.NewEnv(t)
	testenv.RequireTool(t, "tmux")
	// The socket directory does not exist: tmux cannot create the server
	// (and exits 0 doing so). Short path: the macOS socket limit is ~104 bytes.
	bad := "/tmp/agentctl-missing-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "/sock"
	env := agentEnv(base.With(tmux.EnvSocket+"="+bad), sleepAgent)
	repo := testenv.TempRepo(t, env)
	ctl := openController(t, env)
	ctx := context.Background()

	res, err := ctl.NewTask(ctx, NewTaskRequest{Name: "demo", Goal: "g", Cwd: repo.Root})
	if !errors.Is(err, ErrLaunchFailed) || strings.Contains(err.Error(), "\n") {
		t.Fatalf("NewTask = %v, want one-line ErrLaunchFailed", err)
	}
	if _, serr := os.Stat(res.Worktree); serr != nil {
		t.Fatalf("worktree not kept: %v", serr)
	}
	task, terr := ctl.store.Tasks().Get(ctx, "demo")
	if terr != nil || task.State != domain.TaskRunning {
		t.Fatalf("task = %+v %v", task, terr)
	}
	sess, _ := ctl.store.Sessions().LatestForTask(ctx, "demo")
	if sess.State != domain.StateFailed || !strings.Contains(sess.LastDetail, "launch failed") || !strings.Contains(sess.LastDetail, "error creating") {
		t.Fatalf("session = %+v", sess)
	}
	if got := eventTypes(t, ctl, "demo"); strings.Join(got, ",") != "task.created,session.launch_failed" {
		t.Fatalf("events = %v", got)
	}
	ov, _ := ctl.Overview(ctx, OverviewQuery{Cwd: repo.Root})
	if len(ov.Rows) != 1 || ov.Rows[0].Attention.Section != domain.SectionNeedsYou || ov.Rows[0].Attention.Label != "failed" {
		t.Fatalf("overview = %+v", ov.Rows)
	}
	if _, err := os.Stat(filepath.Dir(bad)); err == nil {
		t.Fatalf("%s was created", filepath.Dir(bad))
	}
}

func TestPreflightUsesConfiguredPATH(t *testing.T) {
	t.Parallel()
	base := testenv.NewEnv(t)
	cases := []struct {
		name  string
		tools []string
		agent []string
		want  error
	}{
		{"git missing", []string{"tmux", "sh"}, sleepAgent, ErrGitMissing},
		{"tmux missing", []string{"git", "sh"}, sleepAgent, ErrTmuxMissing},
		{"agent missing", []string{"git", "tmux"}, []string{"agentctl-no-such-agent"}, ErrAgentMissing},
		{"all present", []string{"git", "tmux", "sh"}, sleepAgent, nil},
	}
	for _, c := range cases {
		env := agentEnv(base.With("PATH="+testenv.BinDir(t, c.tools...)), c.agent)
		ctl := openController(t, env)
		err := ctl.Preflight(context.Background())
		if c.want == nil {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if !errors.Is(err, c.want) || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: Preflight = %v, want one-line %v", c.name, err, c.want)
		}
	}
	bad := openController(t, base.With(EnvAgentCmd+`=not json`))
	if err := bad.Preflight(context.Background()); !errors.Is(err, ErrAgentCmdInvalid) {
		t.Errorf("invalid AGENTCTL_AGENT_CMD = %v", err)
	}
}

func TestValidateGoal(t *testing.T) {
	for _, g := range []string{"", "fix it", "multi\nline", `$(rm -rf /)`, strings.Repeat("x", MaxGoalLen)} {
		if err := validateGoal(g); err != nil {
			t.Errorf("validateGoal(%.20q) = %v", g, err)
		}
	}
	for _, g := range []string{"-p evil", "--dangerously-skip-permissions", "a\x00b", strings.Repeat("x", MaxGoalLen+1)} {
		if err := validateGoal(g); !errors.Is(err, ErrInvalidGoal) {
			t.Errorf("validateGoal(%.20q) = %v, want ErrInvalidGoal", g, err)
		}
	}
}

func TestOverviewSortsBySectionThenStateSince(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	ctl := openController(t, env)
	ctx := context.Background()
	repo, _ := ctl.store.Repos().Upsert(ctx, "/r/main", "main")
	mk := func(id string, path ...domain.AgentState) {
		t.Helper()
		if _, err := ctl.store.Tasks().Reserve(ctx, store.Task{ID: id, RepoID: repo.ID, Name: id, WorktreePath: "/w/" + id, Branch: "agentctl/" + id}); err != nil {
			t.Fatal(err)
		}
		s, err := ctl.store.Sessions().Create(ctx, store.NewSession{TaskID: id, AgentType: "claude", Origin: domain.OriginLaunched,
			Identity: domain.Identity{Socket: "/s", ServerPID: 1, SessionID: "$1", PaneID: "%1", PanePID: 2},
			State:    domain.StateStarting, Confidence: domain.ConfidenceNone})
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range path {
			if _, err := ctl.store.Sessions().Transition(ctx, s.ID, st, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("run-old")
	mk("idle", domain.StateReady)
	mk("ask", domain.StateAwaitingInput)
	mk("run-new", domain.StateWorking)
	mk("exited", domain.StateStopped)
	if _, err := ctl.store.Tasks().Reserve(ctx, store.Task{ID: "pending", RepoID: repo.ID, Name: "pending", WorktreePath: "/w/p", Branch: "agentctl/p"}); err != nil {
		t.Fatal(err)
	}
	ov, err := ctl.Overview(ctx, OverviewQuery{AllRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range ov.Rows {
		order = append(order, r.ID+":"+string(r.Attention.Section))
	}
	want := "ask:needs_you,idle:idle,exited:idle,run-old:running,run-new:running,pending:unknown"
	if strings.Join(order, ",") != want {
		t.Fatalf("order = %s\nwant    %s", strings.Join(order, ","), want)
	}
	if ov.Counts != (SectionCounts{NeedsYou: 1, Idle: 2, Running: 2, Unknown: 1}) {
		t.Fatalf("counts = %+v", ov.Counts)
	}
	if ov.Header() != "agents / all repos · 2 running · 1 need you" {
		t.Fatalf("header = %q", ov.Header())
	}
	if ov.Rows[2].Attention.Label != "exited" {
		t.Fatalf("stopped label = %q", ov.Rows[2].Attention.Label)
	}
}

type failingRunner struct{}

func (failingRunner) Run(context.Context, tmux.Request) (tmux.Result, error) {
	return tmux.Result{}, errors.New("exec: permission denied")
}

func TestLiveSessionDisconnectedAndUnverified(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	if _, err := f.ctl.NewTask(ctx, NewTaskRequest{Name: "demo", Goal: "g", Cwd: f.repo.Root}); err != nil {
		t.Fatal(err)
	}
	sess, _ := f.ctl.store.Sessions().LatestForTask(ctx, "demo")

	if l, _ := f.ctl.liveSession(ctx, store.Session{}); l.Status != domain.LivenessDisconnected || l.Reason != domain.ReasonNoIdentity {
		t.Fatalf("zero identity = %v", l)
	}
	broken := openController(t, f.env)
	broken.tmuxRunner = failingRunner{}
	if l, p := broken.liveSession(ctx, sess); l.Status != domain.LivenessUnverified || p.PaneID != "" {
		t.Fatalf("probe failure = %v (pane %+v), want unverified", l, p)
	}

	f.srv.Restart(t) // %N reused by a new server with a new pid
	if _, err := f.srv.Client().NewSession(ctx, tmux.NewSessionSpec{Name: "intruder", Dir: f.repo.Root, Argv: []string{"/bin/sleep", "600"}}); err != nil {
		t.Fatal(err)
	}
	if l, p := f.ctl.liveSession(ctx, sess); l.Reason != domain.ReasonIdentityMismatch || p.PaneID != "" {
		t.Fatalf("after restart = %v, pane %+v (a reused %%N must never be returned)", l, p)
	}
	f.srv.Kill(t)
	if l, _ := f.ctl.liveSession(ctx, sess); l.Status != domain.LivenessDisconnected || l.Reason != domain.ReasonServerGone {
		t.Fatalf("after kill = %v", l)
	}
}

func TestTunablesDefaults(t *testing.T) {
	d := DefaultTunables()
	if d.AckWindow != 15*time.Second || d.DeliverSettle != 250*time.Millisecond || d.PasteEnterDelay != 100*time.Millisecond ||
		d.StartingTimeout != 30*time.Second || d.Poll != 300*time.Millisecond || d.Reconcile != 2*time.Second || d.HookBudget != 1800*time.Millisecond {
		t.Fatalf("defaults = %+v", d)
	}
	got := Tunables{AckWindow: time.Second}.withDefaults()
	if got.AckWindow != time.Second || got.Poll != d.Poll {
		t.Fatalf("withDefaults = %+v", got)
	}
}

func TestNewTaskRequiresAbsoluteCwd(t *testing.T) {
	t.Parallel()
	ctl := openController(t, agentEnv(testenv.NewEnv(t), sleepAgent))
	for _, cwd := range []string{"", "relative/dir"} {
		_, err := ctl.NewTask(context.Background(), NewTaskRequest{Name: "demo", Goal: "g", Cwd: cwd})
		if !errors.Is(err, ErrNotARepo) || strings.Contains(err.Error(), "\n") {
			t.Errorf("NewTask(cwd=%q) = %v, want one-line ErrNotARepo (never the process cwd)", cwd, err)
		}
	}
	if count(t, ctl, "tasks") != 0 {
		t.Fatal("partial task row")
	}
}
