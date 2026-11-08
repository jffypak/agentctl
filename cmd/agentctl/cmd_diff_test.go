package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/testenv"
)

// changeWorktree gives the task one commit, a staged file, an unstaged edit,
// an untracked file with a space in its name, and a stat-dirty file (touched,
// unchanged: porcelain `git diff` would rewrite the index for it).
func changeWorktree(t *testing.T, f *cliFixture) string {
	t.Helper()
	wt := filepath.Join(f.repo.WorktreesDir(), "demo")
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(wt, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("committed.txt", "c1\nc2\n")
	testenv.GitIn(t, f.env, wt, "add", "committed.txt")
	testenv.GitIn(t, f.env, wt, "commit", "-q", "-m", "work")
	write("staged.txt", "s\n")
	testenv.GitIn(t, f.env, wt, "add", "staged.txt")
	write("committed.txt", "c1\nc2\nc3\n")
	write("new file.txt", "n1\nn2\nn3")
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(filepath.Join(wt, "README.md"), future, future); err != nil {
		t.Fatal(err)
	}
	return wt
}

// gitSpy puts a `git` wrapper first on PATH that logs GIT_OPTIONAL_LOCKS, the
// subcommand and every argument of each call, then execs the real git.
func gitSpy(t *testing.T, env testenv.Env) (testenv.Env, string) {
	t.Helper()
	real := testenv.RequireTool(t, "git")
	dir := t.TempDir()
	log := filepath.Join(dir, "git.log")
	script := "#!/bin/sh\nprintf '%s\\t%s\\t%s\\n' \"${GIT_OPTIONAL_LOCKS-unset}\" \"$3\" \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return env.With("PATH=" + dir + string(os.PathListSeparator) + env.Get("PATH")), log
}

type worktreeState struct{ index, head, status, files string }

func readState(t *testing.T, f *cliFixture, wt string) worktreeState {
	t.Helper()
	gitDir := testenv.GitIn(t, f.env, wt, "rev-parse", "--absolute-git-dir")
	b, err := os.ReadFile(filepath.Join(gitDir, "index"))
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(gitDir, "index"))
	sum := sha256.Sum256(b)
	var files strings.Builder
	_ = filepath.WalkDir(wt, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			files.WriteString(p + " " + info.ModTime().String() + "\n")
		}
		return nil
	})
	return worktreeState{
		index:  hex.EncodeToString(sum[:]) + " " + fi.ModTime().String(),
		head:   testenv.GitIn(t, f.env, wt, "rev-parse", "HEAD") + " " + testenv.GitIn(t, f.env, wt, "symbolic-ref", "HEAD"),
		status: testenv.GitIn(t, f.env, wt, "ls-files", "--stage", "-z"),
		files:  files.String(),
	}
}

func TestShowAndDiffAreReadOnlyAndUseOptionalLocks0(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	wt := changeWorktree(t, f)
	spyEnv, log := gitSpy(t, f.env)
	before := readState(t, f, wt)

	for _, args := range [][]string{{"show", "demo"}, {"diff", "demo"}, {"diff", "demo", "--stat"}} {
		if r := runCmd(t, spyEnv, f.repo.Root, args...); r.code != 0 {
			t.Fatalf("%q = %+v", args, r)
		}
		if after := readState(t, f, wt); after != before {
			t.Fatalf("%q changed git state:\nbefore %+v\nafter  %+v", args, before, after)
		}
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(calls) < 6 {
		t.Fatalf("git spy saw only %d calls: %q", len(calls), calls)
	}
	for _, c := range calls {
		parts := strings.SplitN(c, "\t", 3)
		if len(parts) != 3 || parts[0] != "0" {
			t.Errorf("git ran without GIT_OPTIONAL_LOCKS=0: %q", c)
			continue
		}
		sub, argv := parts[1], " "+parts[2]+" "
		switch {
		case sub == "add" || sub == "update-index" || sub == "commit" || sub == "checkout" || sub == "stash":
			t.Errorf("show/diff ran a writing git command: %q", c)
		case strings.Contains(argv, " -N ") || strings.Contains(argv, " --intent-to-add "):
			t.Errorf("show/diff used add -N: %q", c)
		case sub == "diff" && !strings.Contains(argv, " --no-index "):
			t.Errorf("porcelain git diff (rewrites the index): %q", c)
		}
	}
}

func TestDiffWritesPlainDiffWhenNotATerminal(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	changeWorktree(t, f)
	r := runCmd(t, f.env, f.repo.Root, "diff", "demo")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("diff = %+v", r)
	}
	for _, want := range []string{
		"diff --git a/committed.txt b/committed.txt", "+c3", "+++ b/staged.txt",
		"diff --git a/new file.txt b/new file.txt", "new file mode", "+n3\n\\ No newline at end of file",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("diff is missing %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "\x1b[") || strings.Contains(r.stdout, "README.md") {
		t.Fatalf("plain diff has color or the stat-dirty file:\n%s", r.stdout)
	}
	if i, j := strings.Index(r.stdout, "b/staged.txt"), strings.Index(r.stdout, "b/new file.txt"); j < i {
		t.Fatal("untracked new-file diffs must follow the tracked diff")
	}
}

func TestDiffStatPrintsSummary(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	changeWorktree(t, f)
	r := runCmd(t, f.env, f.repo.Root, "diff", "--stat", "demo")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("diff --stat = %+v", r)
	}
	for _, want := range []string{
		"diff vs base ", " (main) · HEAD ",
		"  committed  1 commit · 1 file +2 -0", "    committed.txt  +2 -0",
		"  staged     1 file +1 -0", "    staged.txt  +1 -0",
		"  unstaged   1 file +1 -0", "    committed.txt  +1 -0",
		"  untracked  1 file +3", "    new file.txt  +3",
		"  conflicts  none",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("diff --stat is missing %q:\n%s", want, r.stdout)
		}
	}
	fresh := runCmd(t, f.env, f.repo.Root, "new", "fresh", "--goal", "g")
	if fresh.code != 0 {
		t.Fatalf("new fresh = %+v", fresh)
	}
	if r := runCmd(t, f.env, f.repo.Root, "diff", "--stat", "fresh"); r.code != 0 || !strings.HasSuffix(r.stdout, " · no changes\n") {
		t.Fatalf("diff --stat of an unchanged task = %+v", r)
	}
}

// Not parallel: swaps the package-level terminal check (sequential tests
// never overlap parallel ones).
func TestDiffPipesToPagerOnATerminal(t *testing.T) {
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	changeWorktree(t, f)
	orig := stdoutIsTerminal
	stdoutIsTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { stdoutIsTerminal = orig })

	paged := filepath.Join(t.TempDir(), "paged")
	env := f.env.With("OUT="+paged, `AGENTCTL_PAGER=cat > "$OUT"`, "PAGER=false")
	r := runCmd(t, env, f.repo.Root, "diff", "demo")
	if r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("diff on a tty = %+v", r)
	}
	b, err := os.ReadFile(paged)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "committed.txt") || !strings.Contains(string(b), "new file.txt") || !strings.Contains(string(b), "\x1b[") {
		t.Fatalf("pager received %q", b)
	}

	// PAGER is the fallback; a failing pager is one actionable line, exit 1
	r = runCmd(t, f.env.With("PAGER=exit 7"), f.repo.Root, "diff", "demo")
	if r.code != 1 || strings.Count(r.stderr, "\n") != 1 || !strings.Contains(r.stderr, `pager "exit 7"`) {
		t.Fatalf("failing pager = %+v", r)
	}
	// --stat never pages
	stat := runCmd(t, env.With("OUT="+paged+".stat"), f.repo.Root, "diff", "--stat", "demo")
	if stat.code != 0 || !strings.Contains(stat.stdout, "untracked  1 file +3") {
		t.Fatalf("diff --stat on a tty = %+v", stat)
	}
	if _, err := os.Stat(paged + ".stat"); err == nil {
		t.Fatal("diff --stat went through the pager")
	}
}
