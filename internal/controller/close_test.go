package controller

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

func piPaneAlive(t *testing.T, srv *testenv.TmuxServer, id domain.Identity) bool {
	t.Helper()
	snap := srv.Snapshot(t)
	p, ok := snap.Find(id.PaneID)
	return ok && snap.ServerPID == id.ServerPID && p.PanePID == id.PanePID && !p.Dead
}

func TestCloseHidesTaskAndLeavesSessionWorktreeBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	sess := piNewTask(t, f.ctl, f.repo, "demo")
	piNewTask(t, f.ctl, f.repo, "other")
	wt := filepath.Join(f.repo.WorktreesDir(), "demo")

	res, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stopped || res.TaskID != "demo" || res.Worktree != wt || res.Branch != "jpark/demo" || res.SessionName != "agentctl-demo" {
		t.Fatalf("result = %+v", res)
	}
	if !piPaneAlive(t, f.srv, sess.Identity) {
		t.Fatal("close killed the session")
	}
	if got := piSession(t, f.ctl, "demo"); got != sess {
		t.Fatalf("session changed by close:\n%+v\n%+v", sess, got)
	}
	if !strings.Contains(f.repo.Git(t, "worktree", "list", "--porcelain"), "worktree "+wt+"\n") {
		t.Fatal("worktree gone")
	}
	if f.repo.Git(t, "branch", "--list", "jpark/demo") == "" {
		t.Fatal("branch gone")
	}
	if got := eventTypes(t, f.ctl, "demo"); got[len(got)-1] != domain.EventTaskClosed {
		t.Fatalf("events = %v", got)
	}

	ov, _ := f.ctl.Overview(ctx, OverviewQuery{Cwd: f.repo.Root})
	if len(ov.Rows) != 1 || ov.Rows[0].ID != "other" {
		t.Fatalf("ls shows %+v", ov.Rows)
	}
	ov, _ = f.ctl.Overview(ctx, OverviewQuery{Cwd: f.repo.Root, IncludeClosed: true})
	var closed TaskView
	for _, r := range ov.Rows {
		if r.ID == "demo" {
			closed = r
		}
	}
	if closed.Attention.Section != domain.SectionClosed || closed.TaskState != domain.TaskClosed {
		t.Fatalf("ls --closed row = %+v", closed)
	}
	// a closed task drops out of reconcile: killing its pane later records nothing
	before := piDump(t, f.ctl)
	f.srv.Kill(t)
	if err := f.ctl.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if after := piDump(t, f.ctl); after.Sessions[0] != before.Sessions[0] {
		t.Fatalf("closed task's session reconciled: %+v", after.Sessions[0])
	}

	if _, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "demo"}); !errors.Is(err, ErrTaskClosed) || strings.Contains(err.Error(), "\n") {
		t.Fatalf("second close = %v", err)
	}
	if _, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "nope"}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("close missing = %v", err)
	}
}

func TestCloseStopKillsVerifiedLaunchedSession(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	sess := piNewTask(t, f.ctl, f.repo, "demo")
	other := piNewTask(t, f.ctl, f.repo, "other")

	plan, err := f.ctl.PlanClose(ctx, CloseRequest{Name: "demo", Stop: true})
	if err != nil || !plan.Liveness.IsAlive() || plan.Origin != domain.OriginLaunched || plan.Identity != sess.Identity {
		t.Fatalf("plan = %+v %v", plan, err)
	}
	if !piPaneAlive(t, f.srv, sess.Identity) {
		t.Fatal("PlanClose killed something")
	}
	res, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "demo", Stop: true})
	if err != nil || !res.Stopped {
		t.Fatalf("close --stop = %+v %v", res, err)
	}
	if _, ok := f.srv.Snapshot(t).Find(sess.Identity.PaneID); ok {
		t.Fatal("session still running")
	}
	if !piPaneAlive(t, f.srv, other.Identity) {
		t.Fatal("another task's session was killed")
	}
	got := piSession(t, f.ctl, "demo")
	if got.State != domain.StateStopped || got.LastDetail != stopDetail {
		t.Fatalf("session = %+v", got)
	}
	ch := piEvents(t, f.ctl, "demo", domain.EventAgentStateChanged)
	cl := piEvents(t, f.ctl, "demo", domain.EventTaskClosed)
	if len(ch) != 1 || ch[0]["source"] != domain.SourceUser || len(cl) != 1 || cl[0]["stopped"] != true {
		t.Fatalf("events: %v / %v", ch, cl)
	}
	// worktree and branch still kept
	if f.repo.Git(t, "branch", "--list", "jpark/demo") == "" {
		t.Fatal("branch gone")
	}
}

func TestCloseStopOnDeadPaneKeepsExitStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t, []string{"sh", "-c", "exit 3", "agent"})
	ctx := context.Background()
	sess := piNewTask(t, f.ctl, f.repo, "crash")
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		p, _ := f.srv.Snapshot(t).Find(sess.Identity.PaneID)
		return p.Dead, ""
	})
	if _, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "crash", Stop: true}); err != nil {
		t.Fatal(err)
	}
	if got := piSession(t, f.ctl, "crash"); got.State != domain.StateFailed || got.LastDetail != "exit 3" {
		t.Fatalf("session = %s %q, want failed exit 3", got.State, got.LastDetail)
	}
	if _, ok := f.srv.Snapshot(t).Find(sess.Identity.PaneID); ok {
		t.Fatal("dead pane's tmux session not removed")
	}
}

func TestCloseStopRefusesAdoptedAndUnverifiedSessions(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	ctx := context.Background()
	user := f.userPane(t, "work", f.repo.Root, "/bin/sleep", "600")
	if _, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: user.PaneID, Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	demo, _ := f.ctl.store.Tasks().Get(ctx, "demo")
	if _, err := f.ctl.store.Tasks().Reserve(ctx, store.Task{ID: "empty", RepoID: demo.RepoID, WorktreePath: "/w/empty", Branch: "jpark/empty"}); err != nil {
		t.Fatal(err)
	}
	before := piDump(t, f.ctl)
	for name, want := range map[string]string{
		"mine":  "its session was adopted",
		"empty": "has no agent session to stop",
	} {
		_, err := f.ctl.CloseTask(ctx, CloseRequest{Name: name, Stop: true})
		if !errors.Is(err, ErrStopRefused) || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: %v, want one-line refusal containing %q", name, err, want)
		}
	}
	if !piPaneAlive(t, f.srv, domain.Identity{ServerPID: user.ServerPID, PaneID: user.PaneID, PanePID: user.PanePID}) {
		t.Fatal("adopted pane harmed")
	}
	if after := piDump(t, f.ctl); !reflect.DeepEqual(before, after) {
		t.Fatal("refused stop changed state")
	}
	for _, id := range []string{"mine", "empty"} {
		if task, _ := f.ctl.store.Tasks().Get(ctx, id); task.State != domain.TaskRunning {
			t.Fatalf("%s closed by a refused stop", id)
		}
	}
	// plain close of the adopted task is fine and leaves the pane alone
	if _, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "mine"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.srv.Snapshot(t).Find(user.PaneID); !ok {
		t.Fatal("adopted pane gone after plain close")
	}
}

// TestCloseStopRefusesIdentityMismatch: after a server restart the stored
// $N/%N belong to someone else. --stop must refuse, both before reconcile
// noticed (live check) and after (stored disconnected), and never kill the
// pane that reused the ids.
func TestCloseStopRefusesIdentityMismatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	sess := piNewTask(t, f.ctl, f.repo, "demo")
	f.srv.Restart(t)
	intruder, err := f.srv.Client().NewSession(ctx, tmux.NewSessionSpec{Name: "agentctl-demo", Dir: f.repo.Root, Argv: []string{"/bin/sleep", "600"}})
	if err != nil {
		t.Fatal(err)
	}
	if intruder.PaneID != sess.Identity.PaneID || intruder.SessionID != sess.Identity.SessionID {
		t.Fatalf("expected id reuse: %+v vs %+v", intruder, sess.Identity)
	}
	intruderID := domain.Identity{ServerPID: intruder.ServerPID, PaneID: intruder.PaneID, PanePID: intruder.PanePID}
	for _, phase := range []string{"live check", "stored disconnected"} {
		if phase == "stored disconnected" {
			piReconcile(t, f.ctl)
			assertDisconnected(t, f.ctl, "demo", domain.ReasonIdentityMismatch)
		}
		_, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "demo", Stop: true})
		if !errors.Is(err, ErrStopRefused) || !strings.Contains(err.Error(), "identity_mismatch") || strings.Contains(err.Error(), "\n") {
			t.Fatalf("%s: %v", phase, err)
		}
		if !piPaneAlive(t, f.srv, intruderID) {
			t.Fatalf("%s: the pane that reused the ids was killed", phase)
		}
		if task, _ := f.ctl.store.Tasks().Get(ctx, "demo"); task.State != domain.TaskRunning {
			t.Fatalf("%s: task closed by a refused stop", phase)
		}
	}
	// plain close still works for a disconnected task
	if _, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	if !piPaneAlive(t, f.srv, intruderID) {
		t.Fatal("plain close harmed the intruder")
	}
}
