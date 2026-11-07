// Package testenv builds hermetic test fixtures: an isolated environment
// (temp HOME / XDG dirs / AGENTCTL_HOME, isolated git config, no TMUX or
// TMUX_PANE), temp git repos, and isolated tmux servers on a private -L
// socket that are killed in t.Cleanup. No helper here touches the user's tmux
// server, ~/.claude, or real $HOME state (invariant 8).
//
// Parallel tests must not use t.Setenv: pass Env explicitly to whatever runs
// child processes (controller.Config.Environ, tmux.ExecRunner.Env, ...).
package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Env is an immutable KEY=VALUE environment. With/Without return copies.
type Env []string

// Get returns the value of key ("" if unset).
func (e Env) Get(key string) string {
	v, _ := e.Lookup(key)
	return v
}

// Lookup returns the value of key and whether it is set.
func (e Env) Lookup(key string) (string, bool) {
	for i := len(e) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(e[i], "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// With returns a copy with each KEY=VALUE pair set (replacing earlier values).
func (e Env) With(kv ...string) Env {
	drop := map[string]bool{}
	for _, p := range kv {
		k, _, _ := strings.Cut(p, "=")
		drop[k] = true
	}
	out := make(Env, 0, len(e)+len(kv))
	for _, p := range e {
		k, _, _ := strings.Cut(p, "=")
		if !drop[k] {
			out = append(out, p)
		}
	}
	return append(out, kv...)
}

// Without returns a copy with the given keys removed.
func (e Env) Without(keys ...string) Env {
	drop := map[string]bool{}
	for _, k := range keys {
		drop[k] = true
	}
	out := make(Env, 0, len(e))
	for _, p := range e {
		k, _, _ := strings.Cut(p, "=")
		if !drop[k] {
			out = append(out, p)
		}
	}
	return out
}

// Strings returns the environment as a sorted []string copy.
func (e Env) Strings() []string {
	out := append([]string(nil), e...)
	sort.Strings(out)
	return out
}

// Getenv adapts Env to func(string) string lookups.
func (e Env) Getenv(key string) string { return e.Get(key) }

// NewEnv returns a hermetic base environment rooted in fresh temp dirs:
// HOME, XDG_{STATE,CONFIG,CACHE}_HOME and AGENTCTL_HOME are private, git reads
// no global/system config and has a fixed identity, and TMUX / TMUX_PANE are
// absent so the suite behaves the same inside the user's tmux. PATH, TMPDIR
// and the locale come from the test process.
func NewEnv(t testing.TB) Env {
	t.Helper()
	root := realTempDir(t)
	home := filepath.Join(root, "home")
	agentctlHome := filepath.Join(root, "agentctl-home")
	for _, d := range []string{home, agentctlHome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := Env{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"USER=agentctl-test",
		"LANG=en_US.UTF-8",
		"TERM=xterm-256color",
		"XDG_STATE_HOME=" + filepath.Join(root, "xdg-state"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg-config"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "xdg-cache"),
		"AGENTCTL_HOME=" + agentctlHome,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=agentctl test",
		"GIT_AUTHOR_EMAIL=test@agentctl.invalid",
		"GIT_COMMITTER_NAME=agentctl test",
		"GIT_COMMITTER_EMAIL=test@agentctl.invalid",
		"GIT_CEILING_DIRECTORIES=" + root,
	}
	if v := os.Getenv("TMPDIR"); v != "" {
		env = append(env, "TMPDIR="+v)
	}
	return env
}

// TempHome returns a fresh private directory suitable for AGENTCTL_HOME.
func TempHome(t testing.TB) string {
	t.Helper()
	d := filepath.Join(realTempDir(t), "agentctl-home")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

// realTempDir is t.TempDir with symlinks resolved (macOS /var -> /private/var),
// so paths compare equal to what git and tmux report.
func realTempDir(t testing.TB) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// RequireTool skips the test when name is not on the test process PATH.
func RequireTool(t testing.TB, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not on PATH: %v", name, err)
	}
	return p
}

// BinDir returns a temp directory on PATH-ready form containing symlinks to
// exactly the named tools (resolved from the test process PATH), so a test can
// run with e.g. git present but tmux missing. Missing tools skip the test.
func BinDir(t testing.TB, tools ...string) string {
	t.Helper()
	dir := filepath.Join(realTempDir(t), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		src := RequireTool(t, tool)
		if err := os.Symlink(src, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
