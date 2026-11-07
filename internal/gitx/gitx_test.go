package gitx

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestArgBuilders(t *testing.T) {
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"common dir", CommonDirArgs(), []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}},
		{"symbolic head", SymbolicHeadArgs(), []string{"symbolic-ref", "-q", "--short", "HEAD"}},
		{"head sha", HeadSHAArgs(), []string{"rev-parse", "--verify", "-q", "HEAD^{commit}"}},
		{"branch exists", BranchExistsArgs("agentctl/demo"), []string{"show-ref", "--verify", "--quiet", "refs/heads/agentctl/demo"}},
		{"worktree list", WorktreeListArgs(), []string{"worktree", "list", "--porcelain", "-z"}},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	got, err := WorktreeAddArgs("/r-worktrees/demo", "agentctl/demo", "0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"worktree", "add", "-b", "agentctl/demo", "/r-worktrees/demo", "0123abcd"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("WorktreeAddArgs = %q, want %q", got, want)
	}
	for _, bad := range [][3]string{
		{"relative/path", "agentctl/x", "abc"},
		{"/p", "", "abc"},
		{"/p", "-b", "abc"},
		{"/p", "agentctl/x", ""},
		{"/p", "agentctl/x", "--orphan"},
	} {
		if _, err := WorktreeAddArgs(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("WorktreeAddArgs(%q) accepted", bad)
		}
	}
}

func TestEnvSetsOptionalLocksAndStripsRepoOverrides(t *testing.T) {
	g := Git{Env: []string{"PATH=/bin", "GIT_DIR=/evil", "GIT_WORK_TREE=/evil", "GIT_INDEX_FILE=/evil", "GIT_OPTIONAL_LOCKS=1", "KEEP=1"}}
	env := strings.Join(g.env(), "\n")
	for _, want := range []string{"GIT_OPTIONAL_LOCKS=0", "LC_ALL=C", "KEEP=1", "PATH=/bin"} {
		if !strings.Contains(env, want) {
			t.Errorf("env missing %s:\n%s", want, env)
		}
	}
	for _, bad := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_OPTIONAL_LOCKS=1"} {
		if strings.Contains(env, bad) {
			t.Errorf("env still has %s:\n%s", bad, env)
		}
	}
}

func TestParseWorktreeList(t *testing.T) {
	out := "worktree /r\x00HEAD 1111\x00branch refs/heads/main\x00\x00" +
		"worktree /r-worktrees/a b\x00HEAD 2222\x00branch refs/heads/agentctl/a\x00locked\x00\x00" +
		"worktree /r-worktrees/d\x00HEAD 3333\x00detached\x00\x00"
	wts, err := ParseWorktreeList([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []Worktree{
		{Path: "/r", Head: "1111", Branch: "main"},
		{Path: "/r-worktrees/a b", Head: "2222", Branch: "agentctl/a", Locked: true},
		{Path: "/r-worktrees/d", Head: "3333", Detached: true},
	}
	if !reflect.DeepEqual(wts, want) {
		t.Fatalf("ParseWorktreeList = %+v\nwant %+v", wts, want)
	}
	bare, _ := ParseWorktreeList([]byte("worktree /r.git\x00bare\x00\x00"))
	if len(bare) != 1 || !bare[0].Bare {
		t.Fatalf("bare = %+v", bare)
	}
	if _, err := ParseWorktreeList([]byte("HEAD 1\x00\x00")); err == nil {
		t.Fatal("entry without worktree line accepted")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		stderr string
		want   error
	}{
		{"fatal: not a git repository (or any of the parent directories): .git", ErrNotARepo},
		{"fatal: a branch named 'agentctl/x' already exists", ErrBranchExists},
		{"fatal: '/r-worktrees/x' already exists", ErrWorktreePathExists},
	}
	for _, c := range cases {
		err := newError([]string{"x"}, "/d", c.stderr, errors.New("exit status 128"))
		if !errors.Is(err, c.want) {
			t.Errorf("%q not classified as %v", c.stderr, c.want)
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("multi-line error %q", err)
		}
	}
}
