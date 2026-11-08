package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// adoptFixture: an isolated server with one launched task ("demo", a sleep
// agent) and a controller whose candidate commands include "sleep", so that
// plain /bin/sleep panes stand in for running claude panes.
type adoptFixture struct {
	*fixture
	ctl *Controller
}

func newAdoptFixture(t *testing.T) *adoptFixture {
	t.Helper()
	f := newFixture(t, sleepAgent)
	piNewTask(t, f.ctl, f.repo, "demo")
	return &adoptFixture{fixture: f, ctl: openController(t, f.env.With(EnvAdoptCommands+"=claude, sleep"))}
}

// userPane starts a session the user created by hand (not through agentctl).
func (f *adoptFixture) userPane(t *testing.T, name, dir string, argv ...string) tmux.Created {
	t.Helper()
	c, err := f.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{Name: name, Dir: dir, Argv: argv})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// featureWorktree adds a linked worktree on branch feature with one extra
// commit, and returns (worktree root, main HEAD sha, feature HEAD sha).
func (f *adoptFixture) featureWorktree(t *testing.T) (string, string, string) {
	t.Helper()
	wt := filepath.Join(filepath.Dir(f.repo.Root), "feature-wt")
	mainSHA := f.repo.HeadSHA(t)
	f.repo.Git(t, "worktree", "add", "-q", "-b", "feature", wt, mainSHA)
	testenv.GitIn(t, f.env, wt, "commit", "-q", "--allow-empty", "-m", "feature work")
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return wt, mainSHA, testenv.GitIn(t, f.env, wt, "rev-parse", "HEAD")
}

// realDir is a fresh temp dir with symlinks resolved (tmux reports realpaths).
func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAdoptCandidatesExcludeRegisteredPanesByIdentity(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	ctx := context.Background()
	wt, _, _ := f.featureWorktree(t)
	work := f.userPane(t, "work", filepath.Join(wt, "sub"), "/bin/sleep", "600")
	f.userPane(t, "editor", f.repo.Root, "/bin/sh", "-c", "exec cat")

	list, err := f.ctl.ListAdoptCandidates(ctx, CandidateQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Socket != f.srv.Socket || !reflect.DeepEqual(list.Commands, []string{"claude", "sleep"}) {
		t.Fatalf("list = %+v", list)
	}
	if len(list.Panes) != 1 {
		t.Fatalf("candidates = %+v, want only the unregistered sleep pane", list.Panes)
	}
	got := list.Panes[0]
	if got.PaneID != work.PaneID || got.SessionName != "work" || got.WindowIndex != 0 || got.Cwd != filepath.Join(wt, "sub") || got.Command != "sleep" {
		t.Fatalf("candidate = %+v", got)
	}
	all, err := f.ctl.ListAdoptCandidates(ctx, CandidateQuery{All: true})
	if err != nil || len(all.Panes) != 2 || all.Commands != nil {
		t.Fatalf("--all = %+v %v, want work + editor (still never the registered demo pane)", all, err)
	}
	for _, p := range all.Panes {
		if p.SessionName == "agentctl-demo" {
			t.Fatal("registered pane listed")
		}
	}

	if _, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: work.PaneID, Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.ctl.ListAdoptCandidates(ctx, CandidateQuery{}); len(list.Panes) != 0 {
		t.Fatalf("after adopt: %+v", list.Panes)
	}

	// default command list is claude only
	plain := openController(t, f.env)
	if l, _ := plain.ListAdoptCandidates(ctx, CandidateQuery{}); !reflect.DeepEqual(l.Commands, []string{"claude"}) || len(l.Panes) != 0 {
		t.Fatalf("default candidates = %+v", l)
	}
}

func TestAdoptRegistersPaneWithMergeBaseAndEvents(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	ctx := context.Background()
	wt, mainSHA, featSHA := f.featureWorktree(t)
	work := f.userPane(t, "work", filepath.Join(wt, "sub"), "/bin/sleep", "600")

	res, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: work.PaneID, Name: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	wantID := domain.Identity{Socket: f.srv.Socket, ServerPID: work.ServerPID, SessionID: work.SessionID, PaneID: work.PaneID, PanePID: work.PanePID}
	if res.Identity != wantID || res.Worktree != wt || res.Branch != "feature" || res.BaseRef != "main" || res.BaseSHA != mainSHA ||
		res.RepoRoot != f.repo.Root || res.State != domain.StateUnknown || res.Confidence != domain.ConfidenceNone || res.SessionName != "work" {
		t.Fatalf("result = %+v (feature head %s)", res, featSHA)
	}
	task, _ := f.ctl.store.Tasks().Get(ctx, "foo")
	demo, _ := f.ctl.store.Tasks().Get(ctx, "demo")
	if task.RepoID != demo.RepoID || task.WorktreePath != wt || task.BaseSHA != mainSHA || task.State != domain.TaskRunning {
		t.Fatalf("task = %+v", task)
	}
	sess := piSession(t, f.ctl, "foo")
	if sess.Origin != domain.OriginAdopted || sess.Identity != wantID || sess.State != domain.StateUnknown {
		t.Fatalf("session = %+v", sess)
	}
	if got := eventTypes(t, f.ctl, "foo"); strings.Join(got, ",") != "task.created,session.adopted" {
		t.Fatalf("events = %v", got)
	}
	created := piEvents(t, f.ctl, "foo", domain.EventTaskCreated)[0]
	adopted := piEvents(t, f.ctl, "foo", domain.EventSessionAdopted)[0]
	if created["origin"] != "adopted" || created["base_sha"] != mainSHA || adopted["pane_id"] != work.PaneID || adopted["pane_pid"] != float64(work.PanePID) {
		t.Fatalf("payloads %v / %v", created, adopted)
	}

	// listed as UNKNOWN; reconcile keeps an alive adopted pane as is, and the
	// pane is never touched
	piReconcile(t, f.ctl)
	ov, _ := f.ctl.Overview(ctx, OverviewQuery{Cwd: wt})
	var row TaskView
	for _, r := range ov.Rows {
		if r.ID == "foo" {
			row = r
		}
	}
	if row.Attention.Section != domain.SectionUnknown || row.Origin != domain.OriginAdopted || row.AgentState != domain.StateUnknown {
		t.Fatalf("overview row = %+v", row)
	}
	if l, p := f.ctl.liveSession(ctx, sess); !l.IsAlive() || p.PaneID != work.PaneID {
		t.Fatalf("liveSession = %v", l)
	}

	// adopted pane killed -> disconnected{pane_missing}
	if err := f.srv.Client().KillVerifiedSession(ctx, wantID); err != nil {
		t.Fatal(err)
	}
	piReconcile(t, f.ctl)
	assertDisconnected(t, f.ctl, "foo", domain.ReasonPaneMissing)
}

func TestAdoptBaseFallsBackToHEADWithoutMergeBase(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	wt := filepath.Join(filepath.Dir(f.repo.Root), "lonely-wt")
	f.repo.Git(t, "worktree", "add", "-q", "-b", "scratch", wt)
	testenv.GitIn(t, f.env, wt, "checkout", "-q", "--orphan", "lonely")
	testenv.GitIn(t, f.env, wt, "commit", "-q", "--allow-empty", "-m", "unrelated root")
	head := testenv.GitIn(t, f.env, wt, "rev-parse", "HEAD")
	p := f.userPane(t, "lonely", wt, "/bin/sleep", "600")
	res, err := f.ctl.Adopt(context.Background(), AdoptRequest{PaneID: p.PaneID, Name: "lonely"})
	if err != nil {
		t.Fatal(err)
	}
	if res.BaseRef != "HEAD" || res.BaseSHA != head || res.Branch != "lonely" {
		t.Fatalf("result = %+v, want base HEAD %s", res, head)
	}
}

func TestAdoptErrorsAreOneLineAndWriteNothing(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	ctx := context.Background()
	work := f.userPane(t, "work", f.repo.Root, "/bin/sleep", "600")
	outside := f.userPane(t, "scratch", realDir(t), "/bin/sleep", "600")
	dead := f.userPane(t, "dead", f.repo.Root, "/bin/sh", "-c", "exit 0")
	demo := piSession(t, f.ctl, "demo")
	noServer := openController(t, f.env.With(tmux.EnvSocket+"=/tmp/agentctl-test-no-server-pi/none"))

	cases := []struct {
		name string
		ctl  *Controller
		req  AdoptRequest
		kind error
		want string
	}{
		{"pane missing", f.ctl, AdoptRequest{PaneID: "%999", Name: "x1"}, ErrPaneNotFound, "pane %999 not found on tmux server"},
		{"not a pane id", f.ctl, AdoptRequest{PaneID: "7", Name: "x2"}, ErrPaneNotFound, "is not a tmux pane id"},
		{"already registered", f.ctl, AdoptRequest{PaneID: demo.Identity.PaneID, Name: "x3"}, ErrPaneRegistered, `already registered as task "demo"`},
		{"cwd not in git", f.ctl, AdoptRequest{PaneID: outside.PaneID, Name: "x4"}, ErrNotARepo, "is not inside a git repository"},
		{"name taken", f.ctl, AdoptRequest{PaneID: work.PaneID, Name: "demo"}, ErrTaskExists, `task "demo" already exists`},
		{"invalid name", f.ctl, AdoptRequest{PaneID: work.PaneID, Name: "Bad Name"}, ErrInvalidName, "invalid task name"},
		{"no server", noServer, AdoptRequest{PaneID: work.PaneID, Name: "x5"}, ErrNoTmuxServer, "no tmux server is running"},
	}
	for _, c := range cases {
		before := [3]int{count(t, f.ctl, "tasks"), count(t, f.ctl, "sessions"), count(t, f.ctl, "events")}
		_, err := c.ctl.Adopt(ctx, c.req)
		if !errors.Is(err, c.kind) || strings.Contains(err.Error(), "\n") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one-line %v containing %q", c.name, err, c.kind, c.want)
		}
		if after := [3]int{count(t, f.ctl, "tasks"), count(t, f.ctl, "sessions"), count(t, f.ctl, "events")}; after != before {
			t.Errorf("%s: rows %v -> %v", c.name, before, after)
		}
	}
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		p, ok := f.srv.Snapshot(t).Find(dead.PaneID)
		return ok && p.Dead, ""
	})
	if _, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: dead.PaneID, Name: "x6"}); !errors.Is(err, ErrPaneNotFound) || !strings.Contains(err.Error(), "has exited") {
		t.Errorf("dead pane = %v", err)
	}
	// adopting the same pane twice: the second is "already registered"
	if _, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: work.PaneID, Name: "w1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: work.PaneID, Name: "w2"}); !errors.Is(err, ErrPaneRegistered) {
		t.Fatalf("second adopt = %v", err)
	}
}

// TestAdoptAfterRestartTreatsReusedPaneAsNew: a %N reused after a server
// restart is not "registered" (different pids), shows up as a candidate, and
// adopting it binds a NEW task; the old task stays disconnected with its old
// identity (never re-attributed).
func TestAdoptAfterRestartTreatsReusedPaneAsNew(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	ctx := context.Background()
	old := piSession(t, f.ctl, "demo")
	f.srv.Restart(t)
	reused := f.userPane(t, "agentctl-demo", f.repo.Root, "/bin/sleep", "600")
	if reused.PaneID != old.Identity.PaneID {
		t.Fatalf("expected %%N reuse, got %s vs %s", reused.PaneID, old.Identity.PaneID)
	}
	piReconcile(t, f.ctl)
	assertDisconnected(t, f.ctl, "demo", domain.ReasonIdentityMismatch)
	list, err := f.ctl.ListAdoptCandidates(ctx, CandidateQuery{})
	if err != nil || len(list.Panes) != 1 || list.Panes[0].PanePID != reused.PanePID {
		t.Fatalf("candidates = %+v %v", list, err)
	}
	if _, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: reused.PaneID, Name: "again"}); err != nil {
		t.Fatal(err)
	}
	if s := piSession(t, f.ctl, "demo"); s.State != domain.StateDisconnected || s.Identity != old.Identity {
		t.Fatalf("old task re-attributed: %+v", s)
	}
}
