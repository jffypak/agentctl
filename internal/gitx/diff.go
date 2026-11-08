package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Read-only by construction. Porcelain `git diff` against the worktree
// refreshes the index and rewrites .git/index when stat info is stale — even
// with GIT_OPTIONAL_LOCKS=0 (verified on git 2.49). So every read here uses
// plumbing (diff-tree, diff-index, diff-files), which compares content
// without ever writing the index, or `status`, which honours
// GIT_OPTIONAL_LOCKS. Untracked files are shown with `diff --no-index`
// (never `add -N`, which would write the index).

// ErrOutputClosed reports that the reader of a streamed diff went away (a
// pager quit, `| head`); callers treat it as success.
var ErrOutputClosed = errors.New("gitx: diff output closed by reader")

// binarySniffLen is how much of a file git inspects for a NUL byte to decide
// it is binary (xdiff's FIRST_FEW_BYTES).
const binarySniffLen = 8000

// FileChange is one file in a numstat listing.
type FileChange struct {
	Path    string // new path (verbatim; may contain spaces, tabs, newlines)
	OldPath string // rename source, "" otherwise
	Added   int    // lines added (for untracked files: line count)
	Deleted int
	Binary  bool // numstat "-\t-": no line counts
}

// Summary is a task worktree's change set against its base commit.
type Summary struct {
	Base      string       // base commit
	Head      string       // the worktree's HEAD commit
	Commits   int          // commits in base..HEAD
	Committed []FileChange // base -> HEAD (committed on the branch)
	Staged    []FileChange // HEAD -> index, conflicted paths excluded
	Unstaged  []FileChange // index -> worktree, conflicted paths excluded
	Untracked []FileChange // not ignored; Added = line count
	Conflicts []string     // unmerged paths
	Total     []FileChange // base -> worktree for tracked files (the net effect)
}

// Clean reports whether nothing differs from base.
func (s Summary) Clean() bool {
	return s.Commits == 0 && len(s.Committed) == 0 && len(s.Staged) == 0 && len(s.Unstaged) == 0 &&
		len(s.Untracked) == 0 && len(s.Conflicts) == 0 && len(s.Total) == 0
}

// Totals sums a listing: file count, lines added, lines deleted.
func Totals(files []FileChange) (n, added, deleted int) {
	for _, f := range files {
		added += f.Added
		deleted += f.Deleted
	}
	return len(files), added, deleted
}

// CommittedNumstatArgs: what the branch committed since base (tree to tree).
func CommittedNumstatArgs(base string) []string {
	return []string{"diff-tree", "-r", "--numstat", "-z", "-M", base, "HEAD", "--"}
}

// StagedNumstatArgs: index against HEAD.
func StagedNumstatArgs() []string {
	return []string{"diff-index", "--cached", "--numstat", "-z", "-M", "HEAD", "--"}
}

// UnstagedNumstatArgs: worktree against the index. Plumbing compares
// content, so stat-dirty files (touched, unchanged) are not listed.
func UnstagedNumstatArgs() []string { return []string{"diff-files", "--numstat", "-z", "--"} }

// TotalNumstatArgs: worktree against base for tracked files.
func TotalNumstatArgs(base string) []string {
	return []string{"diff-index", "--numstat", "-z", "-M", base, "--"}
}

// CommitCountArgs counts commits in base..HEAD.
func CommitCountArgs(base string) []string {
	return []string{"rev-list", "--count", base + "..HEAD", "--"}
}

// FullDiffArgs is `git diff <base>` (worktree vs base, tracked files) in
// plumbing form.
func FullDiffArgs(base string, color bool) []string {
	return []string{"diff-index", "-p", "-M", colorFlag(color), base, "--"}
}

// UntrackedDiffArgs renders one untracked file as a new-file diff. git exits
// 1 when the files differ, which is the expected outcome here.
func UntrackedDiffArgs(path string, color bool) []string {
	return []string{"diff", "--no-index", colorFlag(color), "--", "/dev/null", path}
}

func colorFlag(color bool) string {
	if color {
		return "--color=always"
	}
	return "--color=never"
}

// DiffSummary computes the change set of the worktree at dir against base:
// committed on the branch, staged, unstaged, untracked (with line counts),
// conflicts, and the net tracked diff. It never writes the index, HEAD, or
// the worktree.
func (g Git) DiffSummary(ctx context.Context, dir, base string) (Summary, error) {
	if err := checkArg("base", base); err != nil {
		return Summary{}, err
	}
	head, err := g.Head(ctx, dir)
	if err != nil {
		return Summary{}, err
	}
	s := Summary{Base: base, Head: head.SHA}
	st, err := g.Status(ctx, dir)
	if err != nil {
		return Summary{}, err
	}
	s.Conflicts = st.Conflicts()
	if s.Committed, err = g.numstat(ctx, dir, CommittedNumstatArgs(base)); err != nil {
		return Summary{}, err
	}
	out, err := g.run(ctx, dir, CommitCountArgs(base)...)
	if err != nil {
		return Summary{}, err
	}
	if s.Commits, err = strconv.Atoi(trim(out)); err != nil {
		return Summary{}, fmt.Errorf("gitx: unexpected rev-list count %q", trim(out))
	}
	conflicted := map[string]bool{}
	for _, p := range s.Conflicts {
		conflicted[p] = true
	}
	staged, err := g.numstat(ctx, dir, StagedNumstatArgs())
	if err != nil {
		return Summary{}, err
	}
	s.Staged = without(staged, conflicted)
	unstaged, err := g.numstat(ctx, dir, UnstagedNumstatArgs())
	if err != nil {
		return Summary{}, err
	}
	s.Unstaged = without(unstaged, conflicted)
	if s.Total, err = g.numstat(ctx, dir, TotalNumstatArgs(base)); err != nil {
		return Summary{}, err
	}
	for _, p := range st.Untracked() {
		fc, err := untrackedChange(dir, p)
		if errors.Is(err, os.ErrNotExist) {
			continue // removed between status and now
		}
		if err != nil {
			return Summary{}, err
		}
		s.Untracked = append(s.Untracked, fc)
	}
	return s, nil
}

// WriteFullDiff streams the worktree's diff against base to w: the tracked
// diff (`git diff <base>` semantics) followed by every untracked file as a
// new-file diff. color forces --color=always (for `less -R`); otherwise
// --color=never. ErrOutputClosed means w's reader went away.
func (g Git) WriteFullDiff(ctx context.Context, dir, base string, color bool, w io.Writer) error {
	if err := checkArg("base", base); err != nil {
		return err
	}
	st, err := g.Status(ctx, dir)
	if err != nil {
		return err
	}
	if err := g.stream(ctx, dir, w, FullDiffArgs(base, color)); err != nil {
		return err
	}
	for _, p := range st.Untracked() {
		if strings.HasSuffix(p, "/") {
			continue // nested repository: listed in the summary, nothing to diff
		}
		if _, err := os.Lstat(filepath.Join(dir, p)); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := g.stream(ctx, dir, w, UntrackedDiffArgs(p, color), 1); err != nil {
			return err
		}
	}
	return nil
}

func (g Git) numstat(ctx context.Context, dir string, args []string) ([]FileChange, error) {
	out, err := g.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	files, err := ParseNumstatZ(out)
	if err != nil {
		return nil, fmt.Errorf("gitx: %s: %w", args[0], err)
	}
	return files, nil
}

// without drops conflicted paths and duplicate records (diff-files lists
// each unmerged stage separately).
func without(files []FileChange, drop map[string]bool) []FileChange {
	seen := map[string]bool{}
	var out []FileChange
	for _, f := range files {
		if drop[f.Path] || seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		out = append(out, f)
	}
	return out
}

// stream runs `git -C dir args...` with stdout going straight to w. Exit
// codes listed in ok also count as success (`diff --no-index` exits 1 when
// the files differ).
func (g Git) stream(ctx context.Context, dir string, w io.Writer, args []string, ok ...int) error {
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("gitx: directory %q is not absolute", dir)
	}
	bin := g.Bin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"-C", dir}, args...)...)
	cmd.Env = g.env()
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &stderr
	err := cmd.Run()
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, isWS := ee.Sys().(syscall.WaitStatus); isWS && ws.Signaled() && ws.Signal() == syscall.SIGPIPE {
			return ErrOutputClosed
		}
		for _, code := range ok {
			if ee.ExitCode() == code {
				return nil
			}
		}
	}
	if errors.Is(err, syscall.EPIPE) {
		return ErrOutputClosed
	}
	return newError(args, dir, stderr.String(), err)
}

// ParseNumstatZ parses `--numstat -z` output. Each record is
// "added\tdeleted\tpath" NUL-terminated; binary files have "-" counts; a
// rename has an empty path field followed by the old and the new path as two
// more NUL-terminated fields. Paths are verbatim (spaces, tabs, newlines).
func ParseNumstatZ(out []byte) ([]FileChange, error) {
	toks, err := splitNUL(out)
	if err != nil {
		return nil, fmt.Errorf("numstat: %w", err)
	}
	var files []FileChange
	for i := 0; i < len(toks); i++ {
		f := strings.SplitN(toks[i], "\t", 3)
		if len(f) != 3 {
			return nil, fmt.Errorf("numstat: malformed record %q", toks[i])
		}
		fc, err := parseCounts(f[0], f[1])
		if err != nil {
			return nil, fmt.Errorf("numstat: record %q: %w", toks[i], err)
		}
		if f[2] != "" {
			fc.Path = f[2]
		} else {
			if i+2 >= len(toks) || toks[i+1] == "" || toks[i+2] == "" {
				return nil, fmt.Errorf("numstat: rename record %q lacks its paths", toks[i])
			}
			fc.OldPath, fc.Path = toks[i+1], toks[i+2]
			i += 2
		}
		files = append(files, fc)
	}
	return files, nil
}

func parseCounts(a, d string) (FileChange, error) {
	if a == "-" && d == "-" {
		return FileChange{Binary: true}, nil
	}
	added, err1 := strconv.Atoi(a)
	deleted, err2 := strconv.Atoi(d)
	if err1 != nil || err2 != nil || added < 0 || deleted < 0 {
		return FileChange{}, fmt.Errorf("bad counts %q %q", a, d)
	}
	return FileChange{Added: added, Deleted: deleted}, nil
}

// untrackedChange describes an untracked path relative to dir.
func untrackedChange(dir, rel string) (FileChange, error) {
	fc := FileChange{Path: rel}
	if strings.HasSuffix(rel, "/") {
		return fc, nil // nested repository
	}
	if !filepath.IsLocal(rel) {
		return FileChange{}, fmt.Errorf("gitx: untracked path %q escapes the worktree", rel)
	}
	lines, binary, err := countLines(filepath.Join(dir, rel))
	if err != nil {
		return FileChange{}, err
	}
	fc.Added, fc.Binary = lines, binary
	return fc, nil
}

// countLines counts lines the way `git diff --numstat` would for a new file:
// newline count, plus one for a final line without a newline. A NUL in the
// first 8000 bytes makes the file binary (no count). A symlink is one line
// (git diffs its target); anything not a regular file counts zero.
func countLines(path string) (lines int, binary bool, err error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return 0, false, err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return 1, false, nil
	case !fi.Mode().IsRegular():
		return 0, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, false, err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var (
		total int
		last  byte
	)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if total < binarySniffLen {
				sniff := chunk[:min(n, binarySniffLen-total)]
				if bytes.IndexByte(sniff, 0) >= 0 {
					return 0, true, nil
				}
			}
			lines += bytes.Count(chunk, []byte{'\n'})
			total += n
			last = chunk[n-1]
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, false, fmt.Errorf("gitx: read %s: %w", path, rerr)
		}
	}
	if total > 0 && last != '\n' {
		lines++
	}
	return lines, false, nil
}
