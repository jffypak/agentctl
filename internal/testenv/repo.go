package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Repo is a temp git repository with one commit on main. Its root is
// <tmp>/repo, so `../repo-worktrees/<task>` stays inside the test's temp dir.
type Repo struct {
	Root string // realpath
	Env  Env
}

// TempRepo creates a repo with an initial commit on branch main using env's
// isolated git configuration. Skips when git is missing.
func TempRepo(t testing.TB, env Env) *Repo {
	t.Helper()
	RequireTool(t, "git")
	root := filepath.Join(realTempDir(t), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Repo{Root: root, Env: env}
	r.Git(t, "init", "-q", "-b", "main")
	r.WriteFile(t, "README.md", "# test repo\n")
	r.Git(t, "add", "README.md")
	r.Git(t, "commit", "-q", "-m", "initial")
	return r
}

// Git runs git in the repo root and returns trimmed stdout; failure fails the test.
func (r *Repo) Git(t testing.TB, args ...string) string {
	t.Helper()
	return GitIn(t, r.Env, r.Root, args...)
}

// GitIn runs git in dir with env and returns trimmed stdout.
func GitIn(t testing.TB, env Env, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env.Strings()
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, stderr)
	}
	return strings.TrimSpace(string(out))
}

// WriteFile writes a file relative to the repo root.
func (r *Repo) WriteFile(t testing.TB, rel, content string) {
	t.Helper()
	p := filepath.Join(r.Root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// HeadSHA is the full sha of HEAD.
func (r *Repo) HeadSHA(t testing.TB) string {
	t.Helper()
	return r.Git(t, "rev-parse", "HEAD")
}

// WorktreesDir is ../<repo>-worktrees, where agentctl creates task worktrees.
func (r *Repo) WorktreesDir() string {
	return filepath.Join(filepath.Dir(r.Root), filepath.Base(r.Root)+"-worktrees")
}
