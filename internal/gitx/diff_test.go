package gitx

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiffArgBuilders(t *testing.T) {
	const base = "0123abcd"
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"committed", CommittedNumstatArgs(base), []string{"diff-tree", "-r", "--numstat", "-z", "-M", base, "HEAD", "--"}},
		{"staged", StagedNumstatArgs(), []string{"diff-index", "--cached", "--numstat", "-z", "-M", "HEAD", "--"}},
		{"unstaged", UnstagedNumstatArgs(), []string{"diff-files", "--numstat", "-z", "--"}},
		{"total", TotalNumstatArgs(base), []string{"diff-index", "--numstat", "-z", "-M", base, "--"}},
		{"commit count", CommitCountArgs(base), []string{"rev-list", "--count", base + "..HEAD", "--"}},
		{"full diff", FullDiffArgs(base, false), []string{"diff-index", "-p", "-M", "--color=never", base, "--"}},
		{"full diff color", FullDiffArgs(base, true), []string{"diff-index", "-p", "-M", "--color=always", base, "--"}},
		{"untracked", UntrackedDiffArgs("a b/-x.txt", false), []string{"diff", "--no-index", "--color=never", "--", "/dev/null", "a b/-x.txt"}},
		{"status", StatusArgs(), []string{"status", "--porcelain=v2", "-z", "--untracked-files=all"}},
	}
	for _, c := range cases {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// Every read in this card goes through plumbing (diff-tree, diff-index,
// diff-files) or status, never porcelain `git diff` against the worktree:
// porcelain diff refreshes and rewrites .git/index even with
// GIT_OPTIONAL_LOCKS=0 (verified on git 2.49). `diff --no-index` never
// reads the index.
func TestDiffArgBuildersNeverUsePorcelainWorktreeDiff(t *testing.T) {
	for _, args := range [][]string{
		CommittedNumstatArgs("b"), StagedNumstatArgs(), UnstagedNumstatArgs(), TotalNumstatArgs("b"),
		FullDiffArgs("b", true), StatusArgs(), CommitCountArgs("b"),
	} {
		if args[0] == "diff" {
			t.Errorf("%q uses porcelain diff (rewrites the index)", args)
		}
		for _, a := range args {
			if a == "add" || a == "-N" || a == "--intent-to-add" || a == "update-index" {
				t.Errorf("%q writes the index", args)
			}
		}
	}
	if u := UntrackedDiffArgs("x", true); u[0] != "diff" || u[1] != "--no-index" {
		t.Fatalf("untracked diff must be --no-index: %q", u)
	}
}

func TestParseNumstatZ(t *testing.T) {
	out := "3\t1\tplain.go\x00" +
		"-\t-\tbin.dat\x00" +
		"0\t0\t\x00old.txt\x00new name.txt\x00" +
		"1\t0\ta b c.txt\x00" +
		"2\t0\tnl\nname.txt\x00" +
		"1\t1\ttab\there.txt\x00" +
		"-\t-\t\x00a.bin\x00dir/b.bin\x00" +
		"5\t0\t-dash.txt\x00"
	got, err := ParseNumstatZ([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []FileChange{
		{Path: "plain.go", Added: 3, Deleted: 1},
		{Path: "bin.dat", Binary: true},
		{Path: "new name.txt", OldPath: "old.txt"},
		{Path: "a b c.txt", Added: 1},
		{Path: "nl\nname.txt", Added: 2},
		{Path: "tab\there.txt", Added: 1, Deleted: 1},
		{Path: "dir/b.bin", OldPath: "a.bin", Binary: true},
		{Path: "-dash.txt", Added: 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseNumstatZ =\n%+v\nwant\n%+v", got, want)
	}
	if got, err := ParseNumstatZ(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v %v", got, err)
	}
	for _, bad := range []string{
		"x\t1\tf\x00",                // non-numeric count
		"1\t-\tf\x00",                // half binary
		"1\t1\x00",                   // missing path field
		"0\t0\t\x00old.txt\x00",      // rename missing its new path
		"0\t0\t\x00\x00new.txt\x00",  // rename with an empty old path
		"1\t1\tf",                    // unterminated record
		"1\t1\tf\x00\x00",            // empty record
		"-1\t0\tneg.txt\x00",         // negative count
		"1\t1\tf\x00" + "0\t0\t\x00", // truncated rename at the end
	} {
		if got, err := ParseNumstatZ([]byte(bad)); err == nil {
			t.Errorf("ParseNumstatZ(%q) = %+v, want an error", bad, got)
		}
	}
}

func TestParseStatusZ(t *testing.T) {
	out := "1 .M N... 100644 100644 100644 aaaa aaaa mod file.txt\x00" +
		"1 A. N... 000000 100644 100644 0000 bbbb staged.txt\x00" +
		"2 R. N... 100644 100644 100644 cccc cccc R100 new name.txt\x00old name.txt\x00" +
		"u UU N... 100644 100644 100644 100644 h1 h2 h3 conf lict.txt\x00" +
		"u AA N... 000000 100644 100644 100644 h1 h2 h3 both added.txt\x00" +
		"? un tracked.txt\x00" +
		"? nl\nname.txt\x00" +
		"? nested/\x00" +
		"! ignored.log\x00"
	st, err := ParseStatusZ([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []StatusEntry{
		{Kind: '1', X: '.', Y: 'M', Path: "mod file.txt"},
		{Kind: '1', X: 'A', Y: '.', Path: "staged.txt"},
		{Kind: '2', X: 'R', Y: '.', Path: "new name.txt", OrigPath: "old name.txt"},
		{Kind: 'u', X: 'U', Y: 'U', Path: "conf lict.txt"},
		{Kind: 'u', X: 'A', Y: 'A', Path: "both added.txt"},
		{Kind: '?', Path: "un tracked.txt"},
		{Kind: '?', Path: "nl\nname.txt"},
		{Kind: '?', Path: "nested/"},
		{Kind: '!', Path: "ignored.log"},
	}
	if !reflect.DeepEqual(st.Entries, want) {
		t.Fatalf("ParseStatusZ =\n%+v\nwant\n%+v", st.Entries, want)
	}
	if got := st.Conflicts(); !reflect.DeepEqual(got, []string{"both added.txt", "conf lict.txt"}) {
		t.Fatalf("Conflicts = %q", got)
	}
	if got := st.Untracked(); !reflect.DeepEqual(got, []string{"nested/", "nl\nname.txt", "un tracked.txt"}) {
		t.Fatalf("Untracked = %q", got)
	}
	if st, err := ParseStatusZ([]byte("# branch.oid abc\x00")); err != nil || len(st.Entries) != 0 {
		t.Fatalf("header only = %+v %v", st, err)
	}
	for _, bad := range []string{
		"1 .M N... 100644 100644\x00",                 // short ordinary record
		"2 R. N... 1 1 1 c c R100 new.txt\x00",        // rename without its original path
		"u UU N... 1 1 1 1 h h\x00",                   // short unmerged record
		"? \x00",                                      // untracked without a path
		"Z what\x00",                                  // unknown kind
		"1 .M N... 100644 100644 100644 a a f.txt",    // unterminated
		"1 M N... 100644 100644 100644 a a f.txt\x00", // XY is not two bytes
	} {
		if st, err := ParseStatusZ([]byte(bad)); err == nil {
			t.Errorf("ParseStatusZ(%q) = %+v, want an error", bad, st)
		}
	}
}

func TestCountLines(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	big := make([]byte, 100_000)
	for i := range big {
		big[i] = 'x'
		if i%10 == 9 {
			big[i] = '\n'
		}
	}
	lateNUL := append(append([]byte{}, big[:9000]...), 0)
	cases := []struct {
		name, content string
		lines         int
		binary        bool
	}{
		{"empty", "", 0, false},
		{"one terminated", "a\n", 1, false},
		{"one unterminated", "a", 1, false},
		{"three mixed", "a\nb\nc", 3, false},
		{"blank lines", "\n\n", 2, false},
		{"nul is binary", "ab\x00cd\n", 0, true},
		{"big", string(big), 10_000, false},
		// git only sniffs the first 8000 bytes; a later NUL is text
		{"late nul is text", string(lateNUL), 901, false},
	}
	for _, c := range cases {
		n, bin, err := countLines(write(c.name, c.content))
		if err != nil || n != c.lines || bin != c.binary {
			t.Errorf("%s: countLines = %d %v %v, want %d %v", c.name, n, bin, err, c.lines, c.binary)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink("target/elsewhere", link); err != nil {
		t.Fatal(err)
	}
	if n, bin, err := countLines(link); err != nil || n != 1 || bin {
		t.Errorf("symlink = %d %v %v, want 1 line (git diffs the link target)", n, bin, err)
	}
	if n, bin, err := countLines(dir); err != nil || n != 0 || bin {
		t.Errorf("directory = %d %v %v, want 0", n, bin, err)
	}
}

func TestTotals(t *testing.T) {
	files, added, deleted := Totals([]FileChange{{Added: 3, Deleted: 1}, {Binary: true}, {Added: 2}})
	if files != 3 || added != 5 || deleted != 1 {
		t.Fatalf("Totals = %d %d %d", files, added, deleted)
	}
}
