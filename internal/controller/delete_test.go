package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

func TestDeleteStopsAgentRemovesWorktreeAndBranchAndFreesName(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	sess := piNewTask(t, f.ctl, f.repo, "demo")
	wt := filepath.Join(f.repo.WorktreesDir(), "demo")
	testenv.GitIn(t, f.env, wt, "commit", "-q", "--allow-empty", "-m", "work")
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := f.ctl.PlanDelete(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Owned || !plan.Stop || !plan.RemoveWorktree || !plan.DeleteBranch || plan.DirtyFiles != 1 ||
		plan.UnmergedCommits != 1 || plan.Branch != "jpark/demo" || plan.Worktree != wt {
		t.Fatalf("plan = %+v", plan)
	}
	lines := strings.Join(DeletePlanLines(plan), "\n")
	for _, want := range []string{"kills tmux session jpark-demo", "1 uncommitted or untracked file will be lost", "1 commit not on main", "the name is free again"} {
		if !strings.Contains(lines, want) {
			t.Errorf("plan lines lack %q:\n%s", want, lines)
		}
	}
	if !piPaneAlive(t, f.srv, sess.Identity) {
		t.Fatal("PlanDelete changed something")
	}

	res, err := f.ctl.DeleteTask(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stopped || !res.WorktreeRemoved || !res.BranchDeleted {
		t.Fatalf("result = %+v", res)
	}
	if piPaneAlive(t, f.srv, sess.Identity) {
		t.Fatal("agent still runs")
	}
	if _, err := os.Lstat(wt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still there: %v", err)
	}
	if f.repo.Git(t, "branch", "--list", "jpark/demo") != "" {
		t.Fatal("branch still there")
	}
	if n := count(t, f.ctl, "tasks"); n != 0 {
		t.Fatalf("%d task rows left", n)
	}
	if n := count(t, f.ctl, "sessions"); n != 0 {
		t.Fatalf("%d session rows left", n)
	}
	if evs := eventTypes(t, f.ctl, "demo"); len(evs) != 0 {
		t.Fatalf("events still on the freed name: %v", evs)
	}

	// the name is free: a new task of the same name starts clean
	piNewTask(t, f.ctl, f.repo, "demo")
	if evs := eventTypes(t, f.ctl, "demo"); evs[0] != domain.EventTaskCreated || len(evs) != 2 {
		t.Fatalf("new demo events = %v", evs)
	}
}

func TestDeleteClosedTaskWithMissingWorktree(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	piNewTask(t, f.ctl, f.repo, "gone")
	if _, err := f.ctl.CloseTask(ctx, CloseRequest{Name: "gone", Stop: true}); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(f.repo.WorktreesDir(), "gone")
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctl.NewTask(ctx, NewTaskRequest{Name: "gone", Goal: "g", Cwd: f.repo.Root}); !errors.Is(err, ErrTaskExists) ||
		!strings.Contains(err.Error(), "agentctl close --delete gone") {
		t.Fatalf("new over a closed task = %v", err)
	}
	plan, err := f.ctl.PlanDelete(ctx, "gone")
	if err != nil || !plan.Closed || plan.Stop || plan.RemoveWorktree || !plan.WorktreeMissing || !plan.DeleteBranch {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if _, err := f.ctl.DeleteTask(ctx, "gone"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.repo.Git(t, "worktree", "list", "--porcelain"), wt) {
		t.Fatal("stale worktree record not pruned")
	}
	piNewTask(t, f.ctl, f.repo, "gone")
}

func TestDeleteAdoptedTaskKeepsGitAndRefusesLiveAgent(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	ctx := context.Background()
	wt, _, _ := f.featureWorktree(t)
	// the agent exits once the gate file exists
	gate := filepath.Join(realDir(t), "gate")
	work := f.userPane(t, "work", wt, "/bin/sh", "-c", `while [ ! -e "$1" ]; do sleep 0.05; done`, "sh", gate)
	if _, err := f.ctl.Adopt(ctx, AdoptRequest{PaneID: work.PaneID, Name: "foo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ctl.DeleteTask(ctx, "foo"); !errors.Is(err, ErrDeleteRefused) || !strings.Contains(err.Error(), "still runs") {
		t.Fatalf("delete of a live adopted agent = %v", err)
	}
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, 0, "adopted pane exits", func() (bool, string) {
		p, ok := f.srv.Snapshot(t).Find(work.PaneID)
		return !ok || p.Dead, ""
	})
	plan, err := f.ctl.PlanDelete(ctx, "foo")
	if err != nil || plan.Owned || plan.RemoveWorktree || plan.DeleteBranch || len(plan.Kept) != 2 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if _, err := f.ctl.DeleteTask(ctx, "foo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("adopted worktree removed: %v", err)
	}
	if f.repo.Git(t, "branch", "--list", "feature") == "" {
		t.Fatal("adopted branch deleted")
	}
}

func TestAutoAdoptNamesPanesAfterTheirDirectory(t *testing.T) {
	t.Parallel()
	f := newAdoptFixture(t)
	ctx := context.Background()
	wt, _, _ := f.featureWorktree(t)
	a := f.userPane(t, "one", wt, "/bin/sleep", "600")
	b := f.userPane(t, "two", wt, "/bin/sleep", "600")
	f.userPane(t, "nogit", realDir(t), "/bin/sleep", "600")
	f.userPane(t, "shell", wt, "/bin/sh", "-c", "exec cat")

	res, err := f.ctl.AutoAdopt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range res.Adopted {
		got[r.Identity.PaneID] = r.TaskID
	}
	if len(got) != 2 || got[a.PaneID] != "feature-wt" || got[b.PaneID] != "feature-wt-2" {
		t.Fatalf("adopted = %v", got)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "not inside a git repository") {
		t.Fatalf("skipped = %v", res.Skipped)
	}
	again, err := f.ctl.AutoAdopt(ctx)
	if err != nil || len(again.Adopted) != 0 {
		t.Fatalf("second pass = %+v, %v", again, err)
	}
}

func TestNumberedNameStaysASlug(t *testing.T) {
	long := strings.Repeat("a", domain.MaxSlugLen)
	for _, n := range []int{1, 2, 12} {
		if err := domain.ValidateSlug(numberedName(long, n)); err != nil {
			t.Errorf("numberedName(long, %d) = %q: %v", n, numberedName(long, n), err)
		}
	}
	if got := numberedName("x", 3); got != "x-3" {
		t.Errorf("numberedName = %q", got)
	}
}
