package gitx_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/gitx"
	"github.com/jffypak/agentctl/internal/testenv"
)

// inspectRepo builds a task worktree with every kind of change the summary
// reports, relative to base:
//
//	committed on the branch: rename old.txt -> "new name.txt", binary bin.dat,
//	  a file whose name contains a newline, a merge commit (conf.txt)
//	conflict:   conf.txt (UU) after merging a diverging branch
//	staged:     staged.txt (new, 1 line)
//	unstaged:   notes.md (+1 -0 vs the index)
//	untracked:  "un tracked.txt" (3 lines, no final newline), "deep/dir/x.go" (2 lines), "blob.bin" (binary)
//	stat-dirty: README.md (mtime moved, content unchanged)
type inspectRepo struct {
	repo *testenv.Repo
	wt   string
	base string
	env  testenv.Env
}

func newInspectRepo(t *testing.T) inspectRepo {
	t.Helper()
	env := testenv.NewEnv(t)
	repo := testenv.TempRepo(t, env)
	repo.WriteFile(t, "old.txt", "a\nb\nc\nd\ne\nf\ng\nh\n")
	repo.WriteFile(t, "conf.txt", "x\n")
	repo.WriteFile(t, "notes.md", "one\n")
	repo.Git(t, "add", ".")
	repo.Git(t, "commit", "-q", "-m", "fixtures")
	base := repo.HeadSHA(t)

	wt := filepath.Join(repo.WorktreesDir(), "task")
	repo.Git(t, "worktree", "add", "-q", "-b", "agentctl/task", wt, base)
	in := func(args ...string) string { return testenv.GitIn(t, env, wt, args...) }
	write := func(rel, content string) {
		p := filepath.Join(wt, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	in("mv", "old.txt", "new name.txt")
	write("bin.dat", "\x00\x01\x02binary")
	write("nl\nname.txt", "l1\nl2\n")
	in("add", "-A")
	in("commit", "-q", "-m", "branch work")

	in("checkout", "-q", "-b", "side", base)
	write("conf.txt", "side\n")
	in("commit", "-q", "-a", "-m", "side")
	in("checkout", "-q", "agentctl/task")
	write("conf.txt", "task\n")
	in("commit", "-q", "-a", "-m", "task conf")
	merge := exec.Command("git", "merge", "-q", "side")
	merge.Dir, merge.Env = wt, env.Strings()
	if out, err := merge.CombinedOutput(); err == nil {
		t.Fatalf("merge unexpectedly succeeded:\n%s", out)
	}

	write("staged.txt", "staged\n")
	in("add", "staged.txt")
	write("notes.md", "one\ntwo\n")
	write("un tracked.txt", "u1\nu2\nu3")
	write("deep/dir/x.go", "package x\n\n")
	write("blob.bin", "\x00\x00")
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(filepath.Join(wt, "README.md"), future, future); err != nil {
		t.Fatal(err)
	}
	return inspectRepo{repo: repo, wt: wt, base: base, env: env}
}

// gitState is everything a read-only command must leave untouched.
type gitState struct {
	indexHash  string
	indexMTime time.Time
	head       string
	status     string
	files      string
}

func snapshotState(t *testing.T, r inspectRepo) gitState {
	t.Helper()
	gitDir := testenv.GitIn(t, r.env, r.wt, "rev-parse", "--absolute-git-dir")
	idx := filepath.Join(gitDir, "index")
	b, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(idx)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	var files strings.Builder
	_ = filepath.WalkDir(r.wt, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.Name() == ".git" {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files.WriteString(p + " " + info.ModTime().String() + " " + info.Mode().String() + "\n")
		}
		return nil
	})
	if _, err := os.Stat(idx + ".lock"); err == nil {
		t.Fatal("index.lock left behind")
	}
	return gitState{
		indexHash:  hex.EncodeToString(sum[:]),
		indexMTime: fi.ModTime(),
		head:       testenv.GitIn(t, r.env, r.wt, "rev-parse", "HEAD"),
		// plumbing ls-files reads the index without refreshing it
		status: testenv.GitIn(t, r.env, r.wt, "ls-files", "--stage", "-z"),
		files:  files.String(),
	}
}

func TestDiffSummaryIntegration(t *testing.T) {
	t.Parallel()
	r := newInspectRepo(t)
	g, ctx := gitx.Git{Env: r.env.Strings()}, context.Background()
	before := snapshotState(t, r)

	s, err := g.DiffSummary(ctx, r.wt, r.base)
	if err != nil {
		t.Fatal(err)
	}
	if s.Base != r.base || s.Head != testenv.GitIn(t, r.env, r.wt, "rev-parse", "HEAD") || s.Commits != 2 {
		t.Fatalf("base/head/commits = %s %s %d", s.Base, s.Head, s.Commits)
	}
	wantCommitted := []gitx.FileChange{
		{Path: "bin.dat", Binary: true},
		{Path: "conf.txt", Added: 1, Deleted: 1},
		{Path: "new name.txt", OldPath: "old.txt"},
		{Path: "nl\nname.txt", Added: 2},
	}
	if !reflect.DeepEqual(s.Committed, wantCommitted) {
		t.Errorf("committed =\n%+v\nwant\n%+v", s.Committed, wantCommitted)
	}
	if want := []gitx.FileChange{{Path: "staged.txt", Added: 1}}; !reflect.DeepEqual(s.Staged, want) {
		t.Errorf("staged = %+v, want %+v (conflicted conf.txt belongs in Conflicts only)", s.Staged, want)
	}
	if want := []gitx.FileChange{{Path: "notes.md", Added: 1}}; !reflect.DeepEqual(s.Unstaged, want) {
		t.Errorf("unstaged = %+v, want %+v (stat-dirty README.md and conflicted conf.txt excluded)", s.Unstaged, want)
	}
	wantUntracked := []gitx.FileChange{
		{Path: "blob.bin", Binary: true},
		{Path: "deep/dir/x.go", Added: 2},
		{Path: "un tracked.txt", Added: 3},
	}
	if !reflect.DeepEqual(s.Untracked, wantUntracked) {
		t.Errorf("untracked =\n%+v\nwant\n%+v", s.Untracked, wantUntracked)
	}
	if want := []string{"conf.txt"}; !reflect.DeepEqual(s.Conflicts, want) {
		t.Errorf("conflicts = %q, want %q", s.Conflicts, want)
	}
	byPath := map[string]gitx.FileChange{}
	for _, f := range s.Total {
		byPath[f.Path] = f
	}
	for _, p := range []string{"bin.dat", "conf.txt", "new name.txt", "nl\nname.txt", "staged.txt", "notes.md"} {
		if _, ok := byPath[p]; !ok {
			t.Errorf("total vs base is missing %q: %+v", p, s.Total)
		}
	}
	if _, ok := byPath["README.md"]; ok {
		t.Errorf("total lists stat-dirty README.md: %+v", s.Total)
	}
	if after := snapshotState(t, r); after != before {
		t.Fatalf("DiffSummary changed git state:\nbefore %+v\nafter  %+v", before, after)
	}

	// Negative control: porcelain `git diff` rewrites the index here even
	// with GIT_OPTIONAL_LOCKS=0 (stat-dirty README.md), so the check above
	// has teeth and plumbing is required.
	porcelain := exec.Command("git", "diff", "--quiet")
	porcelain.Dir, porcelain.Env = r.wt, r.env.With("GIT_OPTIONAL_LOCKS=0").Strings()
	_ = porcelain.Run()
	if after := snapshotState(t, r); after.indexHash == before.indexHash {
		t.Fatal("negative control: porcelain git diff did not rewrite the index; the read-only check proves nothing")
	}
}

func TestWriteFullDiffIncludesUntrackedAsNewFiles(t *testing.T) {
	t.Parallel()
	r := newInspectRepo(t)
	g, ctx := gitx.Git{Env: r.env.Strings()}, context.Background()
	before := snapshotState(t, r)

	var plain bytes.Buffer
	if err := g.WriteFullDiff(ctx, r.wt, r.base, false, &plain); err != nil {
		t.Fatal(err)
	}
	out := plain.String()
	for _, want := range []string{
		"diff --git a/old.txt b/new name.txt",
		"rename to new name.txt",
		"Binary files /dev/null and b/bin.dat differ",
		"+<<<<<<< HEAD",
		"+++ b/staged.txt",
		"+two",
		"--- /dev/null\n+++ b/un tracked.txt",
		"+u3\n\\ No newline at end of file",
		"+++ b/deep/dir/x.go",
		"Binary files /dev/null and b/blob.bin differ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("full diff is missing %q", want)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("plain diff contains color escapes")
	}
	if strings.Contains(out, "README.md") {
		t.Error("stat-dirty README.md appears in the diff")
	}
	if i, j := strings.Index(out, "b/staged.txt"), strings.Index(out, "b/un tracked.txt"); i < 0 || j < i {
		t.Error("untracked files must follow the tracked diff")
	}

	var color bytes.Buffer
	if err := g.WriteFullDiff(ctx, r.wt, r.base, true, &color); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(color.String(), "\x1b[") {
		t.Error("color diff has no color escapes")
	}
	if after := snapshotState(t, r); after != before {
		t.Fatalf("WriteFullDiff changed git state:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestDiffSummaryCleanTaskAndErrors(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	repo := testenv.TempRepo(t, env)
	g, ctx := gitx.Git{Env: env.Strings()}, context.Background()
	base := repo.HeadSHA(t)
	s, err := g.DiffSummary(ctx, repo.Root, base)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Clean() || s.Commits != 0 {
		t.Fatalf("fresh repo summary = %+v, want no changes", s)
	}
	var buf bytes.Buffer
	if err := g.WriteFullDiff(ctx, repo.Root, base, false, &buf); err != nil || buf.Len() != 0 {
		t.Fatalf("unchanged full diff = %q %v", buf.String(), err)
	}
	if _, err := g.DiffSummary(ctx, repo.Root, "--evil"); err == nil {
		t.Fatal("flag-like base accepted")
	}
	if _, err := g.DiffSummary(ctx, repo.Root, ""); err == nil {
		t.Fatal("empty base accepted")
	}
	if _, err := g.DiffSummary(ctx, filepath.Join(repo.Root, "missing"), base); err == nil {
		t.Fatal("missing worktree accepted")
	}
	if err := g.WriteFullDiff(ctx, "relative", base, false, &buf); err == nil {
		t.Fatal("relative dir accepted")
	}
	if _, err := g.DiffSummary(ctx, repo.Root, strings.Repeat("0", 40)); err == nil {
		t.Fatal("unknown base accepted")
	}
}
