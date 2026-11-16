package gitx

import (
	"context"
	"fmt"
	"path/filepath"
)

// This file is the only place gitx may spell destructive git commands
// (archtest rule gitx-nondestructive). Only controller.DeleteTask calls
// them, after the user confirmed a delete that named the worktree and branch.

// WorktreeRemoveArgs removes a linked worktree. --force also removes a
// worktree with uncommitted changes or untracked files; the caller warns the
// user first. A locked worktree still fails (that needs a second --force).
func WorktreeRemoveArgs(path string) ([]string, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("gitx: worktree path %q is not absolute", path)
	}
	return []string{"worktree", "remove", "--force", path}, nil
}

// WorktreePruneArgs drops worktree records whose directory is gone.
func WorktreePruneArgs() []string { return []string{"worktree", "prune"} }

// BranchDeleteArgs deletes a local branch, merged or not. git still refuses
// a branch that some worktree has checked out.
func BranchDeleteArgs(branch string) ([]string, error) {
	if err := checkArg("branch", branch); err != nil {
		return nil, err
	}
	return []string{"branch", "-D", branch}, nil
}

// RemoveWorktree runs `git worktree remove --force <path>` in repoRoot.
func (g Git) RemoveWorktree(ctx context.Context, repoRoot, path string) error {
	args, err := WorktreeRemoveArgs(path)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, repoRoot, args...)
	return err
}

// PruneWorktrees runs `git worktree prune` in repoRoot.
func (g Git) PruneWorktrees(ctx context.Context, repoRoot string) error {
	_, err := g.run(ctx, repoRoot, WorktreePruneArgs()...)
	return err
}

// DeleteBranch runs `git branch -D <branch>` in repoRoot.
func (g Git) DeleteBranch(ctx context.Context, repoRoot, branch string) error {
	args, err := BranchDeleteArgs(branch)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, repoRoot, args...)
	return err
}
