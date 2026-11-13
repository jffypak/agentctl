package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// noisyAgent prints terminal escapes (OSC 52 clipboard write, OSC title,
// CSI colors / cursor moves), 60 numbered lines with blank lines between
// them, then idles.
var noisyAgent = []string{"sh", "-c", `printf 'boot\033]52;c;ZXZpbA==\007 ok\n\033]0;title\007\033[31mred\033[0m\033[2;1H\n'; ` +
	`i=1; while [ $i -le 60 ]; do printf 'line %d\n\n' $i; i=$((i+1)); done; echo READY; exec sleep 600`, "agent"}

func newInspectFixture(t *testing.T, agent []string) (*fixture, string) {
	t.Helper()
	f := newFixture(t, agent)
	if _, err := f.ctl.NewTask(context.Background(), NewTaskRequest{Name: "demo", Goal: "g", Cwd: f.repo.Root}); err != nil {
		t.Fatal(err)
	}
	return f, filepath.Join(f.repo.WorktreesDir(), "demo")
}

func waitForOutput(t *testing.T, c *Controller, task, want string) Inspection {
	t.Helper()
	var in Inspection
	testenv.WaitFor(t, 0, "pane output "+want, func() (bool, string) {
		var err error
		in, err = c.Inspect(context.Background(), task, InspectOptions{})
		return err == nil && strings.Contains(strings.Join(in.Output, "\n"), want), fmt.Sprintf("%+v", in.Pane)
	})
	return in
}

// mutateWorktree commits, stages, edits and adds an untracked file.
func mutateWorktree(t *testing.T, env testenv.Env, wt string) {
	t.Helper()
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(wt, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("committed.txt", "c1\nc2\n")
	testenv.GitIn(t, env, wt, "add", "committed.txt")
	testenv.GitIn(t, env, wt, "commit", "-q", "-m", "work")
	write("staged.txt", "s\n")
	testenv.GitIn(t, env, wt, "add", "staged.txt")
	write("README.md", "# test repo\nedited\n")
	write("new file.txt", "n1\nn2\nn3\n")
}

func indexHash(t *testing.T, env testenv.Env, wt string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testenv.GitIn(t, env, wt, "rev-parse", "--absolute-git-dir"), "index"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestInspectLiveTaskOutputAndDiff(t *testing.T) {
	t.Parallel()
	f, wt := newInspectFixture(t, noisyAgent)
	mutateWorktree(t, f.env, wt)
	before := indexHash(t, f.env, wt)

	in := waitForOutput(t, f.ctl, "demo", "READY")
	if !in.Pane.Available || in.Pane.Note != "alive" || in.Pane.Liveness.Status != domain.LivenessAlive {
		t.Fatalf("pane = %+v", in.Pane)
	}
	if in.Task.ID != "demo" || in.Task.Worktree != wt || !in.Task.HasSession || in.Task.Identity.PaneID == "" {
		t.Fatalf("task view = %+v", in.Task)
	}
	if len(in.Output) != DefaultInspectLines {
		t.Fatalf("output has %d lines, want %d: %q", len(in.Output), DefaultInspectLines, in.Output)
	}
	if in.Output[len(in.Output)-1] != "READY" || in.Output[0] != "line 22" {
		t.Fatalf("output window = %q ... %q, want line 22 ... READY", in.Output[0], in.Output[len(in.Output)-1])
	}
	for _, l := range in.Output {
		if strings.TrimSpace(l) == "" || strings.ContainsAny(l, "\x1b\x07\r") || strings.Contains(l, "52;c") {
			t.Fatalf("output line %q is empty or carries escapes", l)
		}
	}

	if in.DiffErr != "" {
		t.Fatalf("diff error: %s", in.DiffErr)
	}
	d := in.Diff
	if d.Commits != 1 || d.BaseRef != "main" || d.Base != f.repo.HeadSHA(t) {
		t.Fatalf("diff header = %+v", d)
	}
	check := func(name string, got []FileChange, want []FileChange) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
	check("committed", d.Committed, []FileChange{{Path: "committed.txt", Added: 2}})
	check("staged", d.Staged, []FileChange{{Path: "staged.txt", Added: 1}})
	check("unstaged", d.Unstaged, []FileChange{{Path: "README.md", Added: 1}})
	check("untracked", d.Untracked, []FileChange{{Path: "new file.txt", Added: 3}})
	if len(d.Conflicts) != 0 {
		t.Errorf("conflicts = %q", d.Conflicts)
	}
	if after := indexHash(t, f.env, wt); after != before {
		t.Fatal("Inspect rewrote the worktree index")
	}
}

func TestInspectReusedPaneShowsDiffButNeverCaptures(t *testing.T) {
	t.Parallel()
	f, wt := newInspectFixture(t, []string{"sh", "-c", `echo "demo agent output"; exec sleep 600`, "agent"})
	waitForOutput(t, f.ctl, "demo", "demo agent output")
	sess, _ := f.ctl.store.Sessions().LatestForTask(context.Background(), "demo")
	mutateWorktree(t, f.env, wt)

	f.srv.Restart(t)
	created, err := f.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: "intruder", Dir: t.TempDir(), Argv: []string{"/bin/sh", "-c", `echo "SECRET of another agent"; exec sleep 600`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.PaneID != sess.Identity.PaneID {
		t.Fatalf("restart did not reuse %s (got %s)", sess.Identity.PaneID, created.PaneID)
	}
	testenv.WaitFor(t, 0, "intruder output", func() (bool, string) {
		out := f.srv.Run(t, "capture-pane", "-p", "-t", created.PaneID)
		return strings.Contains(out, "SECRET"), out
	})

	in, err := f.ctl.Inspect(context.Background(), "demo", InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Pane.Available || in.Pane.Note != "pane unavailable (disconnected: identity_mismatch)" || in.Output != nil {
		t.Fatalf("reused pane: %+v output %q", in.Pane, in.Output)
	}
	if in.DiffErr != "" || len(in.Diff.Committed) != 1 || len(in.Diff.Untracked) != 1 {
		t.Fatalf("diff must still be shown: err=%q %+v", in.DiffErr, in.Diff)
	}
	if _, err := f.ctl.CaptureFull(context.Background(), "demo"); !errors.Is(err, ErrPaneUnavailable) ||
		!strings.Contains(err.Error(), "disconnected: identity_mismatch") {
		t.Fatalf("CaptureFull of a reused pane = %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.ctl.paths.Logs, "demo")); len(entries) != 0 {
		t.Fatalf("a log was written for a reused pane: %v", entries)
	}
}

func TestInspectServerGone(t *testing.T) {
	t.Parallel()
	f, _ := newInspectFixture(t, sleepAgent)
	f.srv.Kill(t)
	in, err := f.ctl.Inspect(context.Background(), "demo", InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Pane.Available || in.Pane.Note != "pane unavailable (disconnected: server_gone)" || in.Output != nil || in.DiffErr != "" {
		t.Fatalf("server gone: %+v output=%q diffErr=%q", in.Pane, in.Output, in.DiffErr)
	}
}

// A session already recorded as disconnected stays unavailable even while a
// probe would verify its pane: disconnected is sticky.
func TestInspectStickyDisconnectedIsNeverCaptured(t *testing.T) {
	t.Parallel()
	f, _ := newInspectFixture(t, []string{"sh", "-c", `echo "still here"; exec sleep 600`, "agent"})
	ctx := context.Background()
	waitForOutput(t, f.ctl, "demo", "still here")
	sess, _ := f.ctl.store.Sessions().LatestForTask(ctx, "demo")
	if _, err := f.ctl.store.Sessions().Transition(ctx, sess.ID, domain.StateDisconnected, domain.ConfidenceNone,
		"disconnected: pane_missing", domain.SourcePane); err != nil {
		t.Fatal(err)
	}
	in, err := f.ctl.Inspect(ctx, "demo", InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if in.Pane.Available || in.Pane.Note != "pane unavailable (disconnected: pane_missing)" || in.Output != nil {
		t.Fatalf("sticky disconnected: %+v output=%q", in.Pane, in.Output)
	}
	if _, err := f.ctl.CaptureFull(ctx, "demo"); !errors.Is(err, ErrPaneUnavailable) {
		t.Fatalf("CaptureFull = %v", err)
	}
}

func TestInspectDeadPaneStillCapturesFinalOutput(t *testing.T) {
	t.Parallel()
	f, _ := newInspectFixture(t, []string{"sh", "-c", `echo "final words before crash"; exit 3`, "agent"})
	var in Inspection
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		in, _ = f.ctl.Inspect(context.Background(), "demo", InspectOptions{})
		return in.Pane.Liveness.Status == domain.LivenessDead, in.Pane.Note
	})
	if !in.Pane.Available || in.Pane.Note != "dead (exit 3)" || !strings.Contains(strings.Join(in.Output, "\n"), "final words before crash") {
		t.Fatalf("dead pane: %+v output %q", in.Pane, in.Output)
	}
	path, err := f.ctl.CaptureFull(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "final words before crash") {
		t.Fatalf("full capture of a dead pane = %q", b)
	}
}

func TestCaptureFullWritesPrivateUTCLog(t *testing.T) {
	t.Parallel()
	f, _ := newInspectFixture(t, noisyAgent)
	waitForOutput(t, f.ctl, "demo", "READY")
	path, err := f.ctl.CaptureFull(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.ctl.paths.Logs, "demo")
	if filepath.Dir(path) != dir || !regexp.MustCompile(`^\d{8}T\d{6}\.\d{3}Z\.log$`).MatchString(filepath.Base(path)) {
		t.Fatalf("log path = %s, want %s/<UTC>.log", path, dir)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, dir: 0o700} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s mode = %v %v, want %v", p, fi.Mode().Perm(), err, want)
		}
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	for _, want := range []string{"boot ok", "line 1\n", "line 60", "READY\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("log is missing %q", want)
		}
	}
	if strings.ContainsAny(text, "\x1b\x07\r") || strings.HasSuffix(text, "\n\n") {
		t.Errorf("log carries escapes or trailing blank lines: %q", text)
	}

	// a second capture in the same millisecond never overwrites the first
	clock := time.Date(2026, 11, 7, 1, 2, 3, 4_000_000, time.UTC)
	f.ctl.now = func() time.Time { return clock }
	p1, err1 := f.ctl.CaptureFull(context.Background(), "demo")
	p2, err2 := f.ctl.CaptureFull(context.Background(), "demo")
	if err1 != nil || err2 != nil || p1 == p2 || filepath.Base(p1) != "20261107T010203.004Z.log" {
		t.Fatalf("same-instant captures = %s %v / %s %v", p1, err1, p2, err2)
	}
}

func TestFullDiffCmdPagesTrackedAndUntracked(t *testing.T) {
	t.Parallel()
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	out := filepath.Join(t.TempDir(), "paged")
	env := agentEnv(srv.Env, sleepAgent).With("OUT="+out, `AGENTCTL_PAGER=cat > "$OUT"`)
	f := &fixture{env: env, srv: srv, repo: testenv.TempRepo(t, env)}
	f.ctl = openController(t, env)
	if _, err := f.ctl.NewTask(context.Background(), NewTaskRequest{Name: "demo", Goal: "g", Cwd: f.repo.Root}); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(f.repo.WorktreesDir(), "demo")
	mutateWorktree(t, env, wt)
	before := indexHash(t, env, wt)

	cmd, err := f.ctl.FullDiffCmd(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Stdout != nil || cmd.Stderr != nil || cmd.Stdin == nil {
		t.Fatal("FullDiffCmd must preset only stdin (tea.ExecProcess attaches the terminal)")
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	paged := string(b)
	for _, want := range []string{"committed.txt", "staged.txt", "edited", "new file.txt", "n3"} { // color splits "+" from the text
		if !strings.Contains(paged, want) {
			t.Errorf("paged diff is missing %q", want)
		}
	}
	if !strings.Contains(paged, "\x1b[") {
		t.Error("paged diff should be colored for less -R")
	}
	if entries, _ := os.ReadDir(f.ctl.paths.Logs); len(entries) != 0 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("FullDiffCmd left files in logs/: %v", names)
	}
	if after := indexHash(t, env, wt); after != before {
		t.Fatal("full diff rewrote the index")
	}

	var plain bytes.Buffer
	if err := f.ctl.WriteFullDiff(context.Background(), "demo", &plain, false); err != nil || strings.Contains(plain.String(), "\x1b[") ||
		!strings.Contains(plain.String(), "+++ b/new file.txt") {
		t.Fatalf("WriteFullDiff plain = %v\n%s", err, plain.String())
	}
}

func TestInspectUnknownTask(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	if _, err := f.ctl.Inspect(ctx, "nope", InspectOptions{}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("Inspect = %v", err)
	}
	if _, err := f.ctl.CaptureFull(ctx, "../etc"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("CaptureFull = %v", err)
	}
	if _, err := f.ctl.FullDiffCmd(ctx, "nope"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("FullDiffCmd = %v", err)
	}
	if _, err := f.ctl.DiffSummary(ctx, "nope"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("DiffSummary = %v", err)
	}
}

// fakeTmux answers a snapshot and a conditional capture for one pane, so the
// sanitizer can be tested on bytes tmux itself would never emit.
type fakeTmux struct {
	mu      sync.Mutex
	capture string
	calls   [][]string
}

func (r *fakeTmux) Run(_ context.Context, req tmux.Request) (tmux.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, req.Commands[0])
	switch req.Commands[0][0] {
	case "display-message":
		return tmux.Result{Stdout: []byte("100\n100\t$0\t0\t%3\t200\t0\t\t\tagentctl-demo\tsh\t/x\n")}, nil
	case "if-shell":
		return tmux.Result{Stdout: []byte("agentctl-capture-ok\n" + r.capture)}, nil
	}
	return tmux.Result{}, fmt.Errorf("fakeTmux: unexpected %q", req.Commands)
}

func TestInspectSanitizesCapturedText(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	repo := testenv.TempRepo(t, env)
	fake := &fakeTmux{capture: "ok \x1b]52;c;ZXZpbA==\x07clip\n\x1b[31mred\x1b[0m\r\n\x1b]0;title\x1b\\\n\u009b2Jc1\n   \n\x1bPdcs\x1b\\tail\n"}
	c, err := Open(context.Background(), Config{Environ: env.Strings(), TmuxRunner: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	seedSession(t, c.store, repo, domain.Identity{Socket: "/fake/sock", ServerPID: 100, SessionID: "$0", PaneID: "%3", PanePID: 200})

	in, err := c.Inspect(context.Background(), "demo", InspectOptions{Lines: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ok clip", "red", "c1", "tail"}
	if !reflect.DeepEqual(in.Output, want) {
		t.Fatalf("output = %q, want %q", in.Output, want)
	}
	last := fake.calls[len(fake.calls)-1]
	if last[0] != "if-shell" || !strings.Contains(last[4], "#{pid},100") || !strings.Contains(last[4], "#{pane_pid},200") {
		t.Fatalf("capture was not identity-guarded: %q", last)
	}
	path, err := c.CaptureFull(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.ContainsAny(string(b), "\x1b\x07\r\u009b") {
		t.Fatalf("log carries escapes: %q", b)
	}
}

func seedSession(t *testing.T, st *store.Store, repo *testenv.Repo, id domain.Identity) {
	t.Helper()
	ctx := context.Background()
	r, err := st.Repos().Upsert(ctx, repo.Root, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Tasks().Reserve(ctx, store.Task{ID: "demo", RepoID: r.ID, WorktreePath: repo.Root, Branch: "main",
		BaseRef: "main", BaseSHA: repo.HeadSHA(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Sessions().Create(ctx, store.NewSession{TaskID: "demo", AgentType: "claude", Origin: domain.OriginLaunched,
		Identity: id, SessionName: "jpark-demo", State: domain.StateStarting, Confidence: domain.ConfidenceNone}); err != nil {
		t.Fatal(err)
	}
}

func TestDiffSummaryLines(t *testing.T) {
	s := DiffSummary{
		Base: "0123456789abcdef", BaseRef: "main", Head: "fedcba9876543210", Commits: 2,
		Committed: []FileChange{{Path: "a.go", Added: 3, Deleted: 1}, {Path: "new name.txt", OldPath: "old.txt"}, {Path: "bin.dat", Binary: true}},
		Staged:    []FileChange{{Path: "nl\nname.txt", Added: 1}},
		Untracked: []FileChange{{Path: "u1.txt", Added: 3}, {Path: "u2.txt", Added: 1}, {Path: "u3.txt", Added: 2}},
		Conflicts: []string{"conf.txt"},
		Total:     []FileChange{{Path: "a.go", Added: 3, Deleted: 1}, {Path: "x", Added: 1}},
	}
	got := strings.Join(s.Lines(2), "\n")
	want := strings.Join([]string{
		"diff vs base 01234567 (main) · HEAD fedcba98 · 2 files +4 -1 tracked · 3 untracked · 1 conflict",
		"  committed  2 commits · 3 files +3 -1",
		"    a.go  +3 -1",
		"    old.txt -> new name.txt  +0 -0",
		"    ... 1 more",
		"  staged     1 file +1 -0",
		`    "nl\nname.txt"  +1 -0`,
		"  unstaged   none",
		"  untracked  3 files +6",
		"    u1.txt  +3",
		"    u2.txt  +1",
		"    ... 1 more",
		"  conflicts  1 file",
		"    conf.txt",
	}, "\n")
	if got != want {
		t.Fatalf("Lines =\n%s\nwant\n%s", got, want)
	}
	if all := s.Lines(0); !strings.Contains(strings.Join(all, "\n"), "    bin.dat  binary") {
		t.Fatalf("unlimited Lines = %q", all)
	}
	empty := DiffSummary{Base: "0123456789", BaseRef: "HEAD", Head: "0123456789"}
	if got := empty.Lines(10); len(got) != 1 || got[0] != "diff vs base 01234567 (HEAD) · no changes" {
		t.Fatalf("no-change Lines = %q", got)
	}
}
