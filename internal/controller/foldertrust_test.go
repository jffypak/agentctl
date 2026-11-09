package controller

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// trustAgent prints Claude Code's folder-trust question and then waits, like
// claude on a new worktree before any hook fires.
var trustAgent = []string{"sh", "-c",
	"printf '%s\\n' ' Quick safety check: Is this a project you created or one you' ' trust?' ' ❯ 1. Yes, I trust this folder' '   2. No, exit'; exec sleep 600",
	"agent"}

// recordingRunner records every tmux command name and can fail chosen ones.
type recordingRunner struct {
	inner tmux.Runner
	fail  string // command name to fail ("" = none)

	mu   sync.Mutex
	cmds []string
}

func (r *recordingRunner) Run(ctx context.Context, req tmux.Request) (tmux.Result, error) {
	r.mu.Lock()
	for _, c := range req.Commands {
		if len(c) > 0 {
			r.cmds = append(r.cmds, c[0])
		}
	}
	r.mu.Unlock()
	for _, c := range req.Commands {
		if len(c) > 0 && c[0] == r.fail {
			return tmux.Result{}, errors.New("injected " + r.fail + " failure")
		}
	}
	return r.inner.Run(ctx, req)
}

func (r *recordingRunner) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.cmds {
		if c == name {
			n++
		}
	}
	return n
}

// writesIntoPane reports whether any command could have typed into a pane.
func (r *recordingRunner) writesIntoPane() bool {
	return r.count("send-keys")+r.count("paste-buffer")+r.count("load-buffer") > 0
}

type trustFixture struct {
	srv   *testenv.TmuxServer
	ctl   *Controller
	clock *piClock
	rec   *recordingRunner
}

func newTrustFixture(t *testing.T, agent []string, fail string) *trustFixture {
	t.Helper()
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	env := agentEnv(srv.Env, agent)
	repo := testenv.TempRepo(t, env)
	clock := &piClock{t: time.Now()}
	ctl := piOpen(t, env, Tunables{StartingTimeout: 30 * time.Second}, clock.Now)
	piNewTask(t, ctl, repo, "alpha")
	rec := &recordingRunner{inner: ctl.tmuxRunner, fail: fail}
	ctl.tmuxRunner = rec
	return &trustFixture{srv: srv, ctl: ctl, clock: clock, rec: rec}
}

func (f *trustFixture) waitScreen(t *testing.T, want string) {
	t.Helper()
	pane := piSession(t, f.ctl, "alpha").Identity.PaneID
	testenv.WaitFor(t, 0, "pane shows "+want, func() (bool, string) {
		out, _ := f.srv.TryRun("capture-pane", "-p", "-J", "-t", pane)
		return strings.Contains(out, want), out
	})
}

func overviewRow(t *testing.T, c *Controller, task string) TaskView {
	t.Helper()
	ov, err := c.Overview(context.Background(), OverviewQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ov.Rows {
		if r.ID == task {
			return r
		}
	}
	t.Fatalf("task %s not in the overview", task)
	return TaskView{}
}

// TestReconcileFolderTrustDialogSurfacesWithinOnePoll: a starting agent whose
// pane shows the trust dialog lands in NEEDS YOU on the first reconcile, long
// before StartingTimeout; the timeout never overwrites the specific detail;
// agentctl writes nothing into the pane; the first hook clears it through
// the normal mapping.
func TestReconcileFolderTrustDialogSurfacesWithinOnePoll(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t, trustAgent, "")
	f.waitScreen(t, "one you")

	piReconcile(t, f.ctl) // one poll, the clock has not moved: no timeout involved
	s := piSession(t, f.ctl, "alpha")
	if s.State != domain.StateUnknown || s.Confidence != domain.ConfidenceNone || s.LastDetail != domain.FolderTrustDetail {
		t.Fatalf("after one poll: %s/%s %q, want unknown/none %q", s.State, s.Confidence, s.LastDetail, domain.FolderTrustDetail)
	}
	ev := piEvents(t, f.ctl, "alpha", domain.EventAgentStateChanged)
	if len(ev) != 1 || ev[0]["source"] != domain.SourcePaneText || ev[0]["from"] != "starting" || ev[0]["detail"] != domain.FolderTrustDetail {
		t.Fatalf("events = %v", ev)
	}
	if row := overviewRow(t, f.ctl, "alpha"); row.Attention.Section != domain.SectionNeedsYou || row.LastDetail != domain.FolderTrustDetail {
		t.Fatalf("overview = %+v / %q, want NEEDS YOU with the trust detail", row.Attention, row.LastDetail)
	}

	f.clock.Advance(time.Hour) // StartingTimeout long past: the detail stays specific
	piReconcile(t, f.ctl)
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateUnknown || s.LastDetail != domain.FolderTrustDetail {
		t.Fatalf("after the timeout: %s %q", s.State, s.LastDetail)
	}
	if f.rec.writesIntoPane() {
		t.Fatalf("reconcile wrote into the pane: %v", f.rec.cmds)
	}

	// The user answers; SessionStart arrives and maps as any hook does.
	if _, err := f.ctl.store.Sessions().Transition(context.Background(), s.ID, domain.StateReady, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
		t.Fatalf("SessionStart after the dialog: %v", err)
	}
	piReconcile(t, f.ctl)
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateReady || s.Confidence != domain.ConfidenceHook || s.LastDetail == domain.FolderTrustDetail {
		t.Fatalf("after SessionStart: %s/%s %q", s.State, s.Confidence, s.LastDetail)
	}
}

// TestReconcileFolderTrustOnlyBeforeFirstHook: capture happens only for
// starting agents with no hook yet; a starting agent that already has hook
// evidence (e.g. SessionStart of a goal launch) is never captured or flagged.
func TestReconcileFolderTrustOnlyBeforeFirstHook(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t, trustAgent, "")
	f.waitScreen(t, "one you")
	s := piSession(t, f.ctl, "alpha")
	if _, err := f.ctl.store.Sessions().Transition(context.Background(), s.ID, domain.StateStarting, domain.ConfidenceHook, "submitting goal", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	piReconcile(t, f.ctl)
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateStarting || s.LastDetail != "submitting goal" {
		t.Fatalf("hooked starting agent: %s %q", s.State, s.LastDetail)
	}
	if n := f.rec.count("if-shell"); n != 0 {
		t.Fatalf("captured %d times after the first hook", n)
	}
}

// TestReconcileFolderTrustNoDialogCapturesBounded: an ordinary starting
// agent is captured once per poll, stays starting, and still reaches the
// generic timeout.
func TestReconcileFolderTrustNoDialogCapturesBounded(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t, sleepAgent, "")
	if err := f.ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.rec.count("if-shell"); n != 1 {
		t.Fatalf("captures per poll = %d, want 1", n)
	}
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateStarting {
		t.Fatalf("no dialog: %s", s.State)
	}
	f.clock.Advance(time.Minute)
	piReconcile(t, f.ctl)
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateUnknown || !strings.HasPrefix(s.LastDetail, "no hook within") {
		t.Fatalf("after the timeout: %s %q", s.State, s.LastDetail)
	}
}

// TestReconcileFolderTrustCaptureFailureIsNoMatch: a failing capture is not
// a match and does not fail reconcile; the timeout still applies.
func TestReconcileFolderTrustCaptureFailureIsNoMatch(t *testing.T) {
	t.Parallel()
	f := newTrustFixture(t, trustAgent, "if-shell")
	f.waitScreen(t, "one you")
	if err := f.ctl.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile with a failing capture: %v", err)
	}
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateStarting {
		t.Fatalf("failed capture changed state: %s %q", s.State, s.LastDetail)
	}
	f.clock.Advance(time.Minute)
	if err := f.ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := piSession(t, f.ctl, "alpha"); s.State != domain.StateUnknown || !strings.HasPrefix(s.LastDetail, "no hook within") {
		t.Fatalf("after the timeout: %s %q", s.State, s.LastDetail)
	}
}
