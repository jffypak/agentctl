package gitx_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jffypak/agentctl/internal/gitx"
	"github.com/jffypak/agentctl/internal/testenv"
)

func newGit(env testenv.Env) gitx.Git { return gitx.Git{Env: env.Strings()} }

func TestMainRepoFromRootSubdirAndLinkedWorktree(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	repo := testenv.TempRepo(t, env)
	g, ctx := newGit(env), context.Background()

	sub := filepath.Join(repo.Root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(repo.WorktreesDir(), "linked")
	if err := g.AddWorktree(ctx, repo.Root, wt, "agentctl/linked", repo.HeadSHA(t)); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{repo.Root, sub, wt} {
		info, err := g.MainRepo(ctx, dir)
		if err != nil {
			t.Fatalf("MainRepo(%s): %v", dir, err)
		}
		if info.Root != repo.Root || info.Name != "repo" {
			t.Fatalf("MainRepo(%s) = %+v, want root %s", dir, info, repo.Root)
		}
	}

	notRepo := t.TempDir()
	if _, err := g.MainRepo(ctx, notRepo); !errors.Is(err, gitx.ErrNotARepo) {
		t.Fatalf("MainRepo(non-repo) = %v, want ErrNotARepo", err)
	}
}

func TestHead(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	repo := testenv.TempRepo(t, env)
	g, ctx := newGit(env), context.Background()
	h, err := g.Head(ctx, repo.Root)
	if err != nil || h.Ref != "main" || h.SHA != repo.HeadSHA(t) {
		t.Fatalf("Head = %+v %v", h, err)
	}
	repo.Git(t, "checkout", "-q", "--detach")
	h, err = g.Head(ctx, repo.Root)
	if err != nil || h.Ref != "HEAD" || h.SHA != repo.HeadSHA(t) {
		t.Fatalf("detached Head = %+v %v", h, err)
	}

	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.GitIn(t, env, empty, "init", "-q", "-b", "main")
	if _, err := g.Head(ctx, empty); !errors.Is(err, gitx.ErrNoCommits) {
		t.Fatalf("Head(empty repo) = %v, want ErrNoCommits", err)
	}
}

func TestBranchExistsAndAddWorktree(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	repo := testenv.TempRepo(t, env)
	g, ctx := newGit(env), context.Background()
	base := repo.HeadSHA(t)

	ok, err := g.BranchExists(ctx, repo.Root, "agentctl/demo")
	if err != nil || ok {
		t.Fatalf("BranchExists before = %v %v", ok, err)
	}
	wt := filepath.Join(repo.WorktreesDir(), "demo")
	if err := g.AddWorktree(ctx, repo.Root, wt, "agentctl/demo", base); err != nil {
		t.Fatal(err)
	}
	ok, err = g.BranchExists(ctx, repo.Root, "agentctl/demo")
	if err != nil || !ok {
		t.Fatalf("BranchExists after = %v %v", ok, err)
	}
	if got := testenv.GitIn(t, env, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "agentctl/demo" {
		t.Fatalf("worktree branch = %q", got)
	}
	if got := testenv.GitIn(t, env, wt, "rev-parse", "HEAD"); got != base {
		t.Fatalf("worktree HEAD = %q, want %q", got, base)
	}

	wts, err := g.ListWorktrees(ctx, repo.Root)
	if err != nil || len(wts) != 2 || wts[0].Path != repo.Root || wts[1].Path != wt || wts[1].Branch != "agentctl/demo" {
		t.Fatalf("ListWorktrees = %+v %v", wts, err)
	}

	err = g.AddWorktree(ctx, repo.Root, filepath.Join(repo.WorktreesDir(), "other"), "agentctl/demo", base)
	if !errors.Is(err, gitx.ErrBranchExists) {
		t.Fatalf("AddWorktree existing branch = %v, want ErrBranchExists", err)
	}
	occupied := filepath.Join(repo.WorktreesDir(), "occupied")
	if err := os.MkdirAll(occupied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(occupied, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = g.AddWorktree(ctx, repo.Root, occupied, "agentctl/occupied", base)
	if !errors.Is(err, gitx.ErrWorktreePathExists) {
		t.Fatalf("AddWorktree existing path = %v, want ErrWorktreePathExists", err)
	}
}

func TestToplevelAndMergeBase(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	repo := testenv.TempRepo(t, env)
	g, ctx := newGit(env), context.Background()
	base := repo.HeadSHA(t)
	wt := filepath.Join(repo.WorktreesDir(), "adopted")
	if err := g.AddWorktree(ctx, repo.Root, wt, "agentctl/adopted", base); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(wt, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if top, err := g.Toplevel(ctx, sub); err != nil || top != wt {
		t.Fatalf("Toplevel(%s) = %q %v, want %q", sub, top, err, wt)
	}
	if _, err := g.Toplevel(ctx, t.TempDir()); !errors.Is(err, gitx.ErrNotARepo) {
		t.Fatalf("Toplevel(non-repo) = %v", err)
	}
	// diverge: one commit on main, one on the worktree branch
	repo.WriteFile(t, "main.txt", "m")
	repo.Git(t, "add", "main.txt")
	repo.Git(t, "commit", "-q", "-m", "main moves")
	if err := os.WriteFile(filepath.Join(wt, "wt.txt"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	testenv.GitIn(t, env, wt, "add", "wt.txt")
	testenv.GitIn(t, env, wt, "commit", "-q", "-m", "branch moves")
	mb, err := g.MergeBase(ctx, wt, "HEAD", repo.HeadSHA(t))
	if err != nil || mb != base {
		t.Fatalf("MergeBase = %q %v, want %q", mb, err, base)
	}
	if _, err := g.MergeBase(ctx, wt, "HEAD", "-evil"); err == nil {
		t.Fatal("flag-like rev accepted")
	}
}
