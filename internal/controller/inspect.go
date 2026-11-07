package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/gitx"
	"github.com/jffypak/agentctl/internal/pager"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Inspect (m1-inspect): read-only views of one task — its state block, the
// last lines of its pane, and its changes against base_sha — plus the full
// diff and full capture for a pager. Nothing here writes the registry, the
// pane, or git: capture is identity-checked (liveSession, then a server-side
// check in the same tmux call as capture-pane) and every git read is
// plumbing with GIT_OPTIONAL_LOCKS=0.

// DefaultInspectLines is how many recent non-empty output lines Inspect returns.
const DefaultInspectLines = 40

// recentHistory is how much scrollback Inspect captures to find those lines.
const recentHistory = 2000

// logTimeFormat names full captures: logs/<task>/<UTC>.log.
const logTimeFormat = "20060102T150405.000Z"

// Error kinds of this use case (errors.Is).
var (
	ErrPaneUnavailable = errors.New("pane unavailable")
	ErrDiffUnavailable = errors.New("diff unavailable")
	// ErrOutputClosed: WriteFullDiff's reader went away (`| head`, a pager
	// quit early). Callers treat it as success.
	ErrOutputClosed = gitx.ErrOutputClosed
)

// FileChange is one changed file (paths verbatim; render with DisplayPath).
type FileChange struct {
	Path    string
	OldPath string // rename source
	Added   int    // for untracked files: the line count
	Deleted int
	Binary  bool
}

// DiffSummary is a task's change set against its base commit.
type DiffSummary struct {
	Base      string // base_sha
	BaseRef   string // base_ref at creation ("main", or "HEAD" when detached)
	Head      string // the worktree's HEAD
	Commits   int    // commits in base..HEAD
	Committed []FileChange
	Staged    []FileChange
	Unstaged  []FileChange
	Untracked []FileChange
	Conflicts []string
	Total     []FileChange // tracked changes, base -> worktree
}

// PaneStatus says whether a task's pane may be captured.
type PaneStatus struct {
	Available bool            // identity verified (alive or dead) and not recorded as disconnected
	Liveness  domain.Liveness // zero when there is no session
	Note      string          // "alive", "dead (exit 3)", "pane unavailable (disconnected: identity_mismatch)", ...
}

// Inspection is everything `agentctl show` and the TUI detail view display.
type Inspection struct {
	Task    TaskView
	Pane    PaneStatus
	Output  []string // last N non-empty sanitized lines; nil when the pane is unavailable
	Diff    DiffSummary
	DiffErr string // one line; "" when Diff is valid
}

// InspectOptions tunes Inspect.
type InspectOptions struct {
	Lines int // recent non-empty lines (default DefaultInspectLines)
}

// Inspect verifies the task's pane identity, captures its recent output (only
// when verified), and summarizes its diff against base_sha. A disconnected or
// reused pane still gets the diff. Errors are reserved for an unknown task or
// a registry failure.
func (c *Controller) Inspect(ctx context.Context, taskID string, opts InspectOptions) (Inspection, error) {
	n := opts.Lines
	if n <= 0 {
		n = DefaultInspectLines
	}
	v, sess, err := c.findTask(ctx, taskID)
	if err != nil {
		return Inspection{}, err
	}
	in := Inspection{Task: v}
	text, ps := c.capture(ctx, sess, recentHistory)
	in.Pane = ps
	if ps.Available {
		in.Output = recentLines(text, n)
	}
	if in.Diff, err = c.diffSummary(ctx, v); err != nil {
		in.DiffErr = OneLine(err.Error())
	}
	return in, nil
}

// DiffSummary is the change set of a task's worktree against its base.
func (c *Controller) DiffSummary(ctx context.Context, taskID string) (DiffSummary, error) {
	v, _, err := c.findTask(ctx, taskID)
	if err != nil {
		return DiffSummary{}, err
	}
	return c.diffSummary(ctx, v)
}

// WriteFullDiff writes `git diff <base_sha>` plus every untracked file as a
// new-file diff to w (color for a `less -R` pager). ErrOutputClosed (via
// errors.Is) means w's reader went away.
func (c *Controller) WriteFullDiff(ctx context.Context, taskID string, w io.Writer, color bool) error {
	v, _, err := c.findTask(ctx, taskID)
	if err != nil {
		return err
	}
	if err := checkDiffable(v); err != nil {
		return err
	}
	err = c.git.WriteFullDiff(ctx, v.Worktree, v.BaseSHA, color, w)
	if err == nil || errors.Is(err, ErrOutputClosed) {
		return err
	}
	return userErr(ErrDiffUnavailable, err, "task %s: diff failed: %v", taskID, err)
}

// FullDiffCmd renders the colored full diff and returns the pager command
// that shows it (pager.Resolve: $AGENTCTL_PAGER, $PAGER, less -R), ready for
// tea.ExecProcess. Only Stdin is set: an unlinked temp file under logs/, so
// nothing stays on disk; the caller may Close it after the command exits (it
// is also closed when garbage-collected). Stdout/Stderr are left for the
// caller or ExecProcess to attach to the terminal.
func (c *Controller) FullDiffCmd(ctx context.Context, taskID string) (*exec.Cmd, error) {
	v, _, err := c.findTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := checkDiffable(v); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(c.paths.Logs, ".diff-*")
	if err != nil {
		return nil, fmt.Errorf("cannot buffer the diff: %w", err)
	}
	if err := os.Remove(f.Name()); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("cannot buffer the diff: %w", err)
	}
	if err := c.git.WriteFullDiff(ctx, v.Worktree, v.BaseSHA, true, f); err != nil {
		_ = f.Close()
		return nil, userErr(ErrDiffUnavailable, err, "task %s: diff failed: %v", taskID, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	cmd := pager.Command(c.Getenv, c.env)
	cmd.Stdin = f
	return cmd, nil
}

// PagerCmd returns the pager command showing a file (e.g. CaptureFull's log),
// with Stdin set to it and Stdout/Stderr unset.
func (c *Controller) PagerCmd(path string) (*exec.Cmd, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	cmd := pager.Command(c.Getenv, c.env)
	cmd.Stdin = f
	return cmd, nil
}

// CaptureFull writes the pane's whole scrollback, sanitized, to
// logs/<task>/<UTC>.log (directory 0700, file 0600, never overwritten) and
// returns the path. ErrPaneUnavailable when the identity cannot be verified:
// a disconnected or reused pane is never captured.
func (c *Controller) CaptureFull(ctx context.Context, taskID string) (string, error) {
	_, sess, err := c.findTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	text, ps := c.capture(ctx, sess, tmux.CaptureAll)
	if !ps.Available {
		return "", userErr(ErrPaneUnavailable, nil, "task %s: %s; nothing captured", taskID, ps.Note)
	}
	return c.writeLog(taskID, fullText(text))
}

// findTask returns a task (running or closed) with its newest session.
func (c *Controller) findTask(ctx context.Context, taskID string) (TaskView, *store.Session, error) {
	if domain.ValidateSlug(taskID) == nil {
		rows, err := c.store.Overview(ctx, store.OverviewFilter{IncludeClosed: true})
		if err != nil {
			return TaskView{}, nil, err
		}
		for _, r := range rows {
			if r.Task.ID == taskID {
				return taskView(r, c.now()), r.Session, nil
			}
		}
	}
	return TaskView{}, nil, userErr(ErrTaskNotFound, nil, "task %q not found (`agentctl ls --closed` lists every task)", taskID)
}

// paneStatus verifies identity through liveSession. A session recorded as
// disconnected stays unavailable (sticky) even if a probe would verify.
func (c *Controller) paneStatus(ctx context.Context, sess *store.Session) PaneStatus {
	if sess == nil {
		return PaneStatus{Note: "pane unavailable (no session)"}
	}
	l, _ := c.liveSession(ctx, *sess)
	if sess.State == domain.StateDisconnected {
		if l.Status != domain.LivenessDisconnected {
			l = recordedDisconnect(sess.LastDetail)
		}
		return unavailable(l)
	}
	switch l.Status {
	case domain.LivenessAlive, domain.LivenessDead:
		return PaneStatus{Available: true, Liveness: l, Note: l.String()}
	}
	return unavailable(l)
}

// capture verifies identity, then captures with the server re-checking the
// identity in the same call. A failed capture downgrades the status.
func (c *Controller) capture(ctx context.Context, sess *store.Session, history int) (string, PaneStatus) {
	ps := c.paneStatus(ctx, sess)
	if !ps.Available {
		return "", ps
	}
	id := sess.Identity
	text, err := c.tmuxClient(id.Socket).CapturePane(ctx, tmux.CaptureTarget{PaneID: id.PaneID, ServerPID: id.ServerPID, PanePID: id.PanePID}, history)
	switch {
	case err == nil:
		return text, ps
	case errors.Is(err, tmux.ErrPaneMismatch):
		return "", unavailable(domain.Liveness{Status: domain.LivenessDisconnected, Reason: domain.ReasonIdentityMismatch})
	case errors.Is(err, tmux.ErrPaneNotFound):
		return "", unavailable(domain.Liveness{Status: domain.LivenessDisconnected, Reason: domain.ReasonPaneMissing})
	case errors.Is(err, tmux.ErrNoServer):
		return "", unavailable(domain.Liveness{Status: domain.LivenessDisconnected, Reason: domain.ReasonServerGone})
	}
	return "", unavailable(domain.UnverifiedLiveness(OneLine(err.Error())))
}

func unavailable(l domain.Liveness) PaneStatus {
	return PaneStatus{Liveness: l, Note: "pane unavailable (" + l.String() + ")"}
}

// recordedDisconnect rebuilds the verdict from a stored "disconnected: <reason>" detail.
func recordedDisconnect(detail string) domain.Liveness {
	reason := strings.TrimSpace(strings.TrimPrefix(detail, "disconnected:"))
	if reason == "" || reason == strings.TrimSpace(detail) {
		reason = "recorded"
	}
	return domain.Liveness{Status: domain.LivenessDisconnected, Reason: domain.DisconnectReason(reason)}
}

// captureLines sanitizes captured pane text and splits it into lines with
// trailing whitespace removed.
func captureLines(raw string) []string {
	lines := strings.Split(domain.SanitizeTerminalText(raw), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return lines
}

// recentLines keeps the last n non-blank lines.
func recentLines(raw string, n int) []string {
	out := []string{}
	for _, l := range captureLines(raw) {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// fullText is the whole capture, sanitized, without trailing blank lines.
func fullText(raw string) string {
	lines := captureLines(raw)
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// writeLog creates logs/<task>/<UTC>.log exclusively (a same-millisecond
// capture gets a -N suffix) with mode 0600 in a 0700 directory.
func (c *Controller) writeLog(taskID, content string) (string, error) {
	dir := filepath.Join(c.paths.Logs, taskID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("cannot restrict %s: %w", dir, err)
	}
	stamp := c.now().UTC().Format(logTimeFormat)
	for i := 1; i <= 100; i++ {
		name := stamp + ".log"
		if i > 1 {
			name = stamp + "-" + strconv.Itoa(i) + ".log"
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("cannot create %s: %w", path, err)
		}
		_, werr := f.WriteString(content)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", fmt.Errorf("cannot write %s: %w", path, werr)
		}
		return path, nil
	}
	return "", fmt.Errorf("cannot create a log in %s: too many captures at %s", dir, stamp)
}

func checkDiffable(v TaskView) error {
	if v.BaseSHA == "" {
		return userErr(ErrDiffUnavailable, nil, "task %s has no base commit recorded", v.ID)
	}
	if fi, err := os.Stat(v.Worktree); err != nil || !fi.IsDir() {
		return userErr(ErrDiffUnavailable, err, "worktree %s of task %s is missing", v.Worktree, v.ID)
	}
	return nil
}

func (c *Controller) diffSummary(ctx context.Context, v TaskView) (DiffSummary, error) {
	if err := checkDiffable(v); err != nil {
		return DiffSummary{}, err
	}
	s, err := c.git.DiffSummary(ctx, v.Worktree, v.BaseSHA)
	if err != nil {
		return DiffSummary{}, userErr(ErrDiffUnavailable, err, "diff of task %s failed: %v", v.ID, err)
	}
	return DiffSummary{
		Base: s.Base, BaseRef: v.BaseRef, Head: s.Head, Commits: s.Commits,
		Committed: fileChanges(s.Committed), Staged: fileChanges(s.Staged), Unstaged: fileChanges(s.Unstaged),
		Untracked: fileChanges(s.Untracked), Conflicts: s.Conflicts, Total: fileChanges(s.Total),
	}, nil
}

func fileChanges(in []gitx.FileChange) []FileChange {
	if len(in) == 0 {
		return nil
	}
	out := make([]FileChange, len(in))
	for i, f := range in {
		out[i] = FileChange{Path: f.Path, OldPath: f.OldPath, Added: f.Added, Deleted: f.Deleted, Binary: f.Binary}
	}
	return out
}

// --- rendering (shared by `agentctl show`, `diff --stat` and the TUI) ---------

// Clean reports whether nothing differs from base.
func (s DiffSummary) Clean() bool {
	return s.Commits == 0 && len(s.Committed) == 0 && len(s.Staged) == 0 && len(s.Unstaged) == 0 &&
		len(s.Untracked) == 0 && len(s.Conflicts) == 0 && len(s.Total) == 0
}

// Lines renders the summary: a header, then committed / staged / unstaged /
// untracked / conflicts sections (always all five, "none" when empty), each
// listing at most maxFiles files (<= 0: all). Paths that contain control or
// non-printable characters are Go-quoted, so nothing a filename holds can
// reach the terminal raw.
func (s DiffSummary) Lines(maxFiles int) []string {
	head := "diff vs base " + short(s.Base) + " (" + DisplayPath(s.BaseRef) + ")"
	if s.Clean() {
		return []string{head + " · no changes"}
	}
	n, a, d := totals(s.Total)
	head += " · HEAD " + short(s.Head) + fmt.Sprintf(" · %s +%d -%d tracked", plural(n, "file"), a, d)
	if len(s.Untracked) > 0 {
		head += fmt.Sprintf(" · %d untracked", len(s.Untracked))
	}
	if len(s.Conflicts) > 0 {
		head += " · " + plural(len(s.Conflicts), "conflict")
	}
	out := []string{head}
	section := func(label, summary string, entries []string) {
		out = append(out, fmt.Sprintf("  %-10s %s", label, summary))
		shown := entries
		if maxFiles > 0 && len(entries) > maxFiles {
			shown = entries[:maxFiles]
		}
		for _, e := range shown {
			out = append(out, "    "+e)
		}
		if len(shown) < len(entries) {
			out = append(out, fmt.Sprintf("    ... %d more", len(entries)-len(shown)))
		}
	}
	committed := "none"
	if s.Commits > 0 || len(s.Committed) > 0 {
		committed = plural(s.Commits, "commit") + " · " + changeTotals(s.Committed)
	}
	section("committed", committed, fileLines(s.Committed, false))
	section("staged", orNone(s.Staged, changeTotals), fileLines(s.Staged, false))
	section("unstaged", orNone(s.Unstaged, changeTotals), fileLines(s.Unstaged, false))
	section("untracked", orNone(s.Untracked, untrackedTotals), fileLines(s.Untracked, true))
	conflicts := "none"
	if len(s.Conflicts) > 0 {
		conflicts = plural(len(s.Conflicts), "file")
	}
	paths := make([]string, len(s.Conflicts))
	for i, p := range s.Conflicts {
		paths[i] = DisplayPath(p)
	}
	section("conflicts", conflicts, paths)
	return out
}

func orNone(files []FileChange, render func([]FileChange) string) string {
	if len(files) == 0 {
		return "none"
	}
	return render(files)
}

func changeTotals(files []FileChange) string {
	n, a, d := totals(files)
	return fmt.Sprintf("%s +%d -%d", plural(n, "file"), a, d)
}

func untrackedTotals(files []FileChange) string {
	n, a, _ := totals(files)
	return fmt.Sprintf("%s +%d", plural(n, "file"), a)
}

func fileLines(files []FileChange, untracked bool) []string {
	out := make([]string, len(files))
	for i, f := range files {
		name := DisplayPath(f.Path)
		if f.OldPath != "" {
			name = DisplayPath(f.OldPath) + " -> " + name
		}
		switch {
		case f.Binary:
			out[i] = name + "  binary"
		case untracked && strings.HasSuffix(f.Path, "/"):
			out[i] = name + "  nested repository"
		case untracked:
			out[i] = fmt.Sprintf("%s  +%d", name, f.Added)
		default:
			out[i] = fmt.Sprintf("%s  +%d -%d", name, f.Added, f.Deleted)
		}
	}
	return out
}

func totals(files []FileChange) (n, added, deleted int) {
	for _, f := range files {
		added += f.Added
		deleted += f.Deleted
	}
	return len(files), added, deleted
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// DisplayPath renders a path for a terminal: as is when every rune is
// printable, Go-quoted otherwise (newlines, tabs, escapes, bidi controls,
// invalid UTF-8).
func DisplayPath(p string) string {
	if !utf8.ValidString(p) || strings.IndexFunc(p, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		return strconv.Quote(p)
	}
	return p
}
