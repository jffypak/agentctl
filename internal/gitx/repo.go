package gitx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

// RepoInfo identifies a main repository (never a linked worktree).
type RepoInfo struct {
	Root      string // realpath of the main worktree root
	Name      string // basename of Root
	CommonDir string // realpath of the shared .git directory
}

// Head is the checked-out revision.
type Head struct {
	Ref string // short branch name, or "HEAD" when detached
	SHA string // full commit sha
}

// CommonDirArgs resolves the shared git dir (the main repo's .git even inside
// a linked worktree).
func CommonDirArgs() []string {
	return []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}
}

// SymbolicHeadArgs prints the current branch (exit 1 when detached).
func SymbolicHeadArgs() []string { return []string{"symbolic-ref", "-q", "--short", "HEAD"} }

// HeadSHAArgs prints the HEAD commit (exit 1 with no output when unborn).
func HeadSHAArgs() []string { return []string{"rev-parse", "--verify", "-q", "HEAD^{commit}"} }

// BranchExistsArgs checks for a local branch (exit 1 when absent).
func BranchExistsArgs(branch string) []string {
	return []string{"show-ref", "--verify", "--quiet", "refs/heads/" + branch}
}

// MainRepo resolves the main repository for any directory inside it or inside
// one of its linked worktrees, via --git-common-dir. Running agentctl inside a
// task worktree therefore registers against the main repo.
func (g Git) MainRepo(ctx context.Context, dir string) (RepoInfo, error) {
	out, err := g.run(ctx, dir, CommonDirArgs()...)
	if err != nil {
		return RepoInfo{}, err
	}
	common, err := filepath.EvalSymlinks(trim(out))
	if err != nil {
		return RepoInfo{}, fmt.Errorf("gitx: resolve git dir for %s: %w", dir, err)
	}
	root := ""
	if filepath.Base(common) == ".git" {
		root = filepath.Dir(common)
	} else {
		// separate-git-dir or bare layouts: the first worktree entry is the main one
		wts, err := g.ListWorktrees(ctx, dir)
		if err != nil {
			return RepoInfo{}, err
		}
		if len(wts) == 0 || wts[0].Bare {
			return RepoInfo{}, fmt.Errorf("gitx: %s: %w (bare repository)", dir, ErrUnsupportedRepo)
		}
		if root, err = filepath.EvalSymlinks(wts[0].Path); err != nil {
			return RepoInfo{}, fmt.Errorf("gitx: resolve main worktree for %s: %w", dir, err)
		}
	}
	return RepoInfo{Root: root, Name: filepath.Base(root), CommonDir: common}, nil
}

// Head returns the branch and commit checked out in dir.
func (g Git) Head(ctx context.Context, dir string) (Head, error) {
	out, err := g.run(ctx, dir, HeadSHAArgs()...)
	if err != nil {
		if exitCode(err) == 1 {
			return Head{}, fmt.Errorf("gitx: %s: %w", dir, ErrNoCommits)
		}
		return Head{}, err
	}
	h := Head{SHA: trim(out), Ref: "HEAD"}
	ref, err := g.run(ctx, dir, SymbolicHeadArgs()...)
	switch {
	case err == nil:
		h.Ref = trim(ref)
	case exitCode(err) == 1: // detached
	default:
		return Head{}, err
	}
	return h, nil
}

// BranchExists reports whether refs/heads/<branch> exists in repoRoot.
func (g Git) BranchExists(ctx context.Context, repoRoot, branch string) (bool, error) {
	if err := checkArg("branch", branch); err != nil {
		return false, err
	}
	_, err := g.run(ctx, repoRoot, BranchExistsArgs(branch)...)
	if err == nil {
		return true, nil
	}
	var ge *Error
	if errors.As(err, &ge) && ge.ExitCode == 1 {
		return false, nil
	}
	return false, err
}
