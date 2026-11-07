// Package gitx wraps the git CLI (no libgit2). Phase 1 is non-destructive by
// construction: this package never resets, deletes branches or worktrees,
// cleans, forces, or pushes (archtest enforces the vocabulary). Every command
// runs with GIT_OPTIONAL_LOCKS=0 so reads never take the index lock, and with
// LC_ALL=C so error messages can be classified.
//
// One file per command family: repo.go (main-repo resolution, HEAD, refs),
// worktree.go (worktree add/list). m1-inspect adds diff.go and status.go.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Sentinel errors (errors.Is).
var (
	ErrNotARepo           = errors.New("not a git repository")
	ErrNoCommits          = errors.New("repository has no commits")
	ErrBranchExists       = errors.New("branch already exists")
	ErrWorktreePathExists = errors.New("worktree path already exists")
	ErrUnsupportedRepo    = errors.New("unsupported repository layout")
)

// Git runs git commands.
type Git struct {
	Bin string   // git binary; default "git"
	Env []string // base environment; nil inherits the process env
}

// strippedEnv are variables that would silently redirect git away from the
// directory we pass with -C.
var strippedEnv = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_PREFIX", "GIT_OPTIONAL_LOCKS", "LC_ALL"}

func (g Git) env() []string {
	base := g.Env
	if base == nil {
		base = os.Environ()
	}
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, s := range strippedEnv {
			if k == s {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
}

// run executes `git -C dir args...` and returns stdout. dir must be absolute:
// `git -C ""` (or a relative dir) would silently resolve against the process
// cwd, i.e. whatever repository agentctl happens to run in.
func (g Git) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("gitx: directory %q is not absolute", dir)
	}
	bin := g.Bin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"-C", dir}, args...)...)
	cmd.Env = g.env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), newError(args, dir, stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

// Error is a failed git command.
type Error struct {
	Args     []string
	Dir      string
	Stderr   string // first meaningful stderr line
	ExitCode int
	Err      error
	kind     error
}

func newError(args []string, dir, stderr string, err error) *Error {
	e := &Error{Args: args, Dir: dir, Stderr: firstLine(stderr), Err: err, ExitCode: -1}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		e.ExitCode = ee.ExitCode()
	}
	switch s := strings.ToLower(stderr); {
	case strings.Contains(s, "not a git repository"):
		e.kind = ErrNotARepo
	case strings.Contains(s, "a branch named") && strings.Contains(s, "already exists"):
		e.kind = ErrBranchExists
	case strings.Contains(s, "already exists"):
		e.kind = ErrWorktreePathExists
	}
	return e
}

func (e *Error) Error() string {
	sub := ""
	if len(e.Args) > 0 {
		sub = e.Args[0] + " "
	}
	msg := e.Stderr
	if msg == "" {
		msg = e.Err.Error()
	}
	return "git " + sub + "(in " + e.Dir + "): " + msg
}

// Unwrap exposes the classified sentinel and the exec error.
func (e *Error) Unwrap() []error {
	if e.kind != nil {
		return []error{e.kind, e.Err}
	}
	return []error{e.Err}
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "fatal: ")
		l = strings.TrimPrefix(l, "error: ")
		if l != "" {
			return l
		}
	}
	return ""
}

func exitCode(err error) int {
	var ge *Error
	if errors.As(err, &ge) {
		return ge.ExitCode
	}
	return -1
}

func trim(b []byte) string { return strings.TrimSpace(string(b)) }

func checkArg(name, v string) error {
	if v == "" || strings.HasPrefix(v, "-") || strings.ContainsAny(v, "\x00\n") {
		return fmt.Errorf("gitx: invalid %s %q", name, v)
	}
	return nil
}
