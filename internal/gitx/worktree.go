package gitx

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// Worktree is one entry of `git worktree list --porcelain`.
type Worktree struct {
	Path     string
	Head     string
	Branch   string // short name ("" when detached or bare)
	Bare     bool
	Detached bool
	Locked   bool
}

// WorktreeListArgs lists worktrees NUL-separated (paths may contain newlines).
func WorktreeListArgs() []string { return []string{"worktree", "list", "--porcelain", "-z"} }

// WorktreeAddArgs creates a new branch at base checked out in a new worktree.
func WorktreeAddArgs(path, branch, base string) ([]string, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("gitx: worktree path %q is not absolute", path)
	}
	if err := checkArg("branch", branch); err != nil {
		return nil, err
	}
	if err := checkArg("base", base); err != nil {
		return nil, err
	}
	return []string{"worktree", "add", "-b", branch, path, base}, nil
}

// AddWorktree runs `git worktree add -b <branch> <path> <base>` in repoRoot.
// git creates missing parent directories. ErrBranchExists /
// ErrWorktreePathExists classify the common failures.
func (g Git) AddWorktree(ctx context.Context, repoRoot, path, branch, base string) error {
	args, err := WorktreeAddArgs(path, branch, base)
	if err != nil {
		return err
	}
	_, err = g.run(ctx, repoRoot, args...)
	return err
}

// ListWorktrees lists the worktrees of the repository containing dir; the
// first entry is the main worktree.
func (g Git) ListWorktrees(ctx context.Context, dir string) ([]Worktree, error) {
	out, err := g.run(ctx, dir, WorktreeListArgs()...)
	if err != nil {
		return nil, err
	}
	return ParseWorktreeList(out)
}

// ParseWorktreeList parses `worktree list --porcelain -z` output: attributes
// are NUL-terminated and entries end with an empty attribute.
func ParseWorktreeList(out []byte) ([]Worktree, error) {
	var (
		list []Worktree
		cur  *Worktree
	)
	for _, attr := range bytes.Split(out, []byte{0}) {
		a := string(attr)
		if a == "" {
			if cur != nil {
				list = append(list, *cur)
				cur = nil
			}
			continue
		}
		key, val, _ := strings.Cut(a, " ")
		if key == "worktree" {
			if cur != nil {
				list = append(list, *cur)
			}
			cur = &Worktree{Path: val}
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("gitx: worktree list attribute %q before any worktree line", a)
		}
		switch key {
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = strings.TrimPrefix(val, "refs/heads/")
		case "bare":
			cur.Bare = true
		case "detached":
			cur.Detached = true
		case "locked":
			cur.Locked = true
		}
	}
	if cur != nil {
		list = append(list, *cur)
	}
	return list, nil
}
