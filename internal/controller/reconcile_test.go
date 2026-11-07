package controller

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// piClock is a settable clock for StartingTimeout tests.
type piClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *piClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *piClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func piOpen(t *testing.T, env testenv.Env, tun Tunables, now func() time.Time) *Controller {
	t.Helper()
	c, err := Open(context.Background(), Config{Environ: env.Strings(), AgentctlPath: "/opt/agentctl/bin/agentctl", Tunables: tun, Now: now})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// piDB is every session row plus the event count: what "idempotent" compares.
type piDB struct {
	Sessions []store.Session
	Events   int
}

func piDump(t *testing.T, c *Controller) piDB {
	t.Helper()
	rows, err := c.store.DB().Query(`SELECT id FROM sessions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	_ = rows.Close()
	out := piDB{Events: count(t, c, "events")}
	for _, id := range ids {
		s, err := c.store.Sessions().Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		out.Sessions = append(out.Sessions, s)
	}
	return out
}

// piReconcile runs Reconcile twice and fails unless the second pass changes
// nothing at all (rows and events): the idempotence acceptance bullet.
func piReconcile(t *testing.T, c *Controller) {
	t.Helper()
	ctx := context.Background()
	if err := c.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	first := piDump(t, c)
	if err := c.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if second := piDump(t, c); !reflect.DeepEqual(first, second) {
		t.Fatalf("second Reconcile changed state:\n%+v\n%+v", first, second)
	}
}

func piSession(t *testing.T, c *Controller, task string) store.Session {
	t.Helper()
	s, err := c.store.Sessions().LatestForTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func piNewTask(t *testing.T, c *Controller, repo *testenv.Repo, name string) store.Session {
	t.Helper()
	if _, err := c.NewTask(context.Background(), NewTaskRequest{Name: name, Goal: "g", Cwd: repo.Root}); err != nil {
		t.Fatalf("NewTask %s: %v", name, err)
	}
	return piSession(t, c, name)
}

// piEvents returns the payloads of every event of one type for a task.
func piEvents(t *testing.T, c *Controller, task, typ string) []map[string]any {
	t.Helper()
	evs, err := c.store.Events().ForTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range evs {
		if e.Type == typ {
			var p map[string]any
			_ = json.Unmarshal(e.Payload, &p)
			out = append(out, p)
		}
	}
	return out
}

func assertDisconnected(t *testing.T, c *Controller, task string, reason domain.DisconnectReason) {
	t.Helper()
	s := piSession(t, c, task)
	if s.State != domain.StateDisconnected || s.Confidence != domain.ConfidencePane || s.LastDetail != "disconnected: "+string(reason) {
		t.Fatalf("%s: session = %s/%s %q, want disconnected{%s}", task, s.State, s.Confidence, s.LastDetail, reason)
	}
	disc := piEvents(t, c, task, domain.EventSessionDisconnected)
	if len(disc) != 1 || disc[0]["reason"] != string(reason) {
		t.Fatalf("%s: session.disconnected events = %v", task, disc)
	}
	changed := piEvents(t, c, task, domain.EventAgentStateChanged)
	if len(changed) != 1 || changed[0]["source"] != domain.SourcePane || changed[0]["to"] != "disconnected" {
		t.Fatalf("%s: agent.state_changed = %v", task, changed)
	}
}

func TestReconcilePaneKilledIsDisconnectedPaneMissing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	demo := piNewTask(t, f.ctl, f.repo, "demo")
	piNewTask(t, f.ctl, f.repo, "other")
	if err := f.srv.Client().KillVerifiedSession(ctx, demo.Identity); err != nil {
		t.Fatal(err)
	}
	piReconcile(t, f.ctl)
	assertDisconnected(t, f.ctl, "demo", domain.ReasonPaneMissing)
	if s := piSession(t, f.ctl, "other"); s.State != domain.StateStarting {
		t.Fatalf("other = %s, want starting (untouched)", s.State)
	}
	ov, _ := f.ctl.Overview(ctx, OverviewQuery{Cwd: f.repo.Root})
	if ov.Rows[0].ID != "demo" || ov.Rows[0].Attention != (domain.Attention{Section: domain.SectionNeedsYou, Label: "disconnected"}) {
		t.Fatalf("overview = %+v", ov.Rows[0].Attention)
	}
}

func TestReconcileServerKilledIsDisconnectedServerGone(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	piNewTask(t, f.ctl, f.repo, "demo")
	piNewTask(t, f.ctl, f.repo, "other")
	f.srv.Kill(t)
	piReconcile(t, f.ctl)
	assertDisconnected(t, f.ctl, "demo", domain.ReasonServerGone)
	assertDisconnected(t, f.ctl, "other", domain.ReasonServerGone)
}

func TestReconcileServerRestartReusedPaneIsIdentityMismatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	demo := piNewTask(t, f.ctl, f.repo, "demo")
	f.srv.Restart(t)
	// Same session name, same $N and %N, new server pid and pane pid.
	intruder, err := f.srv.Client().NewSession(ctx, tmux.NewSessionSpec{Name: "agentctl-demo", Dir: f.repo.Root, Argv: []string{"/bin/sleep", "600"}})
	if err != nil {
		t.Fatal(err)
	}
	if intruder.PaneID != demo.Identity.PaneID || intruder.SessionID != demo.Identity.SessionID {
		t.Fatalf("expected id reuse: stored %+v, new %+v", demo.Identity, intruder)
	}
	piReconcile(t, f.ctl)
	assertDisconnected(t, f.ctl, "demo", domain.ReasonIdentityMismatch)

	// The new pane is never bound to any task.
	after := piSession(t, f.ctl, "demo")
	if after.Identity != demo.Identity {
		t.Fatalf("stored identity rewritten: %+v -> %+v", demo.Identity, after.Identity)
	}
	newID := domain.Identity{Socket: f.srv.Socket, ServerPID: intruder.ServerPID, SessionID: intruder.SessionID, PaneID: intruder.PaneID, PanePID: intruder.PanePID}
	if s, err := f.ctl.store.Sessions().PaneOwner(ctx, newID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reused pane bound to session %+v (%v)", s, err)
	}
	if n := count(t, f.ctl, "sessions"); n != 1 {
		t.Fatalf("sessions = %d, want 1", n)
	}
	if l, p := f.ctl.liveSession(ctx, after); l.Reason != domain.ReasonIdentityMismatch || p.PaneID != "" {
		t.Fatalf("liveSession = %v %+v", l, p)
	}
	if p, ok := f.srv.Snapshot(t).Find(intruder.PaneID); !ok || p.Dead {
		t.Fatal("reconcile harmed the reused pane")
	}
}

func TestReconcileDeadPaneExitStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	for name, argv := range map[string][]string{
		"zero":  {"sh", "-c", "exit 0", "agent"},
		"three": {"sh", "-c", "exit 3", "agent"},
	} {
		c := openController(t, agentEnv(f.env, argv))
		s := piNewTask(t, c, f.repo, name)
		testenv.WaitFor(t, 0, name+" dead pane", func() (bool, string) {
			p, _ := f.srv.Snapshot(t).Find(s.Identity.PaneID)
			return p.Dead, ""
		})
	}
	piReconcile(t, f.ctl)
	for name, want := range map[string]struct {
		state  domain.AgentState
		detail string
		label  string
	}{
		"zero":  {domain.StateStopped, "exit 0", "exited"},
		"three": {domain.StateFailed, "exit 3", "failed"},
	} {
		s := piSession(t, f.ctl, name)
		if s.State != want.state || s.LastDetail != want.detail || s.Confidence != domain.ConfidencePane || s.EndedAt.IsZero() {
			t.Errorf("%s: session = %s %q %s, want %s %q", name, s.State, s.LastDetail, s.Confidence, want.state, want.detail)
		}
		if len(piEvents(t, f.ctl, name, domain.EventSessionDisconnected)) != 0 {
			t.Errorf("%s: a dead pane is not a disconnect", name)
		}
	}
	ov, _ := f.ctl.Overview(ctx, OverviewQuery{Cwd: f.repo.Root})
	labels := map[string]string{}
	for _, r := range ov.Rows {
		labels[r.ID] = r.Attention.Label
	}
	if labels["zero"] != "exited" || labels["three"] != "failed" {
		t.Fatalf("labels = %v", labels)
	}
}

// TestReconcileSignalKilledPaneIsFailed documents a skeleton bug found here:
// tmux 3.5a prints pane_dead_signal as a name ("term") and pane_dead_status
// empty for a pane killed by a signal, and tmux/panes.go (skeleton-owned)
// rejects that line, so the whole socket's snapshot fails and every session
// on it stays unverified. Fix proposed in docs/notes/m1-pane-identity.md;
// remove the Skip once panes.go parses signal names.
func TestReconcileSignalKilledPaneIsFailed(t *testing.T) {
	t.Skip("blocked on tmux/panes.go parsing pane_dead_signal names (docs/notes/m1-pane-identity.md, finding 1)")
	t.Parallel()
	f := newFixture(t, []string{"sh", "-c", "kill -TERM $$", "agent"})
	s := piNewTask(t, f.ctl, f.repo, "sig")
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		out, _ := f.srv.TryRun("display-message", "-p", "-t", s.Identity.PaneID, "#{pane_dead}")
		return out == "1", out
	})
	piReconcile(t, f.ctl)
	if got := piSession(t, f.ctl, "sig"); got.State != domain.StateFailed || got.LastDetail != "signal 15" {
		t.Fatalf("session = %s %q, want failed \"signal 15\"", got.State, got.LastDetail)
	}
}

func TestReconcileStartingTimeoutBecomesUnknown(t *testing.T) {
	t.Parallel()
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	env := agentEnv(srv.Env, sleepAgent)
	repo := testenv.TempRepo(t, env)
	clock := &piClock{t: time.Now()}
	ctl := piOpen(t, env, Tunables{StartingTimeout: 2 * time.Second}, clock.Now)
	piNewTask(t, ctl, repo, "demo")

	clock.Advance(1999 * time.Millisecond)
	piReconcile(t, ctl)
	if s := piSession(t, ctl, "demo"); s.State != domain.StateStarting {
		t.Fatalf("before the timeout: %s", s.State)
	}
	clock.Advance(time.Millisecond)
	piReconcile(t, ctl)
	s := piSession(t, ctl, "demo")
	if s.State != domain.StateUnknown || s.Confidence != domain.ConfidenceNone || s.LastDetail != "no hook within 2s" {
		t.Fatalf("after the timeout: %+v", s)
	}
	ev := piEvents(t, ctl, "demo", domain.EventAgentStateChanged)
	if len(ev) != 1 || ev[0]["source"] != domain.SourceTimeout || ev[0]["from"] != "starting" || ev[0]["to"] != "unknown" {
		t.Fatalf("events = %v", ev)
	}
	// unknown is not sticky: the pane dying later is still recorded
	clock.Advance(time.Hour)
	if err := srv.Client().KillVerifiedSession(context.Background(), s.Identity); err != nil {
		t.Fatal(err)
	}
	piReconcile(t, ctl)
	if s := piSession(t, ctl, "demo"); s.State != domain.StateDisconnected {
		t.Fatalf("after the kill: %s", s.State)
	}
}

func TestReconcileTerminalStatesStayPutOnLateHooks(t *testing.T) {
	t.Parallel()
	f := newFixture(t, []string{"sh", "-c", "exit 0", "agent"})
	ctx := context.Background()
	s := piNewTask(t, f.ctl, f.repo, "demo")
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		p, _ := f.srv.Snapshot(t).Find(s.Identity.PaneID)
		return p.Dead, ""
	})
	piReconcile(t, f.ctl)
	before := piDump(t, f.ctl)
	if before.Sessions[0].State != domain.StateStopped {
		t.Fatalf("state = %s", before.Sessions[0].State)
	}
	// A hook for this very session arriving late (e.g. a delayed Stop) is refused.
	for _, to := range []domain.AgentState{domain.StateReady, domain.StateWorking, domain.StateAwaitingInput} {
		if _, err := f.ctl.store.Sessions().Transition(ctx, s.ID, to, domain.ConfidenceHook, "late", domain.SourceHook); !errors.Is(err, store.ErrTransitionNotAllowed) {
			t.Fatalf("late hook -> %s = %v", to, err)
		}
	}
	piReconcile(t, f.ctl)
	if after := piDump(t, f.ctl); !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal session changed:\n%+v\n%+v", before, after)
	}
}

func TestReconcileProbeFailureChangesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	piNewTask(t, f.ctl, f.repo, "demo")
	before := piDump(t, f.ctl)
	broken := openController(t, f.env)
	broken.tmuxRunner = failingRunner{}
	err := broken.Reconcile(context.Background())
	if err == nil {
		t.Fatalf("Reconcile with a failing probe = %v, want an error to warn about", err)
	}
	if after := piDump(t, f.ctl); !reflect.DeepEqual(before, after) {
		t.Fatalf("an unverified probe changed state:\n%+v\n%+v", before, after)
	}
}

func TestReconcileConcurrentReconcilersAppendOneEvent(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	piNewTask(t, f.ctl, f.repo, "a")
	piNewTask(t, f.ctl, f.repo, "b")
	f.srv.Kill(t)
	ctls := []*Controller{f.ctl, openController(t, f.env), openController(t, f.env)}
	var wg sync.WaitGroup
	errs := make([]error, 9)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = ctls[i%len(ctls)].Reconcile(context.Background())
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, task := range []string{"a", "b"} {
		assertDisconnected(t, f.ctl, task, domain.ReasonServerGone)
	}
}

// TestRestartRecoveryFreshController: closing every controller (TUI/CLI exit)
// and opening a new one recovers identical mappings and states, and nothing
// in tmux is killed (invariant 2).
func TestRestartRecoveryFreshController(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	piNewTask(t, f.ctl, f.repo, "one")
	piNewTask(t, f.ctl, f.repo, "two")
	before, err := f.ctl.Overview(ctx, OverviewQuery{Cwd: f.repo.Root})
	if err != nil {
		t.Fatal(err)
	}
	snapBefore := f.srv.Snapshot(t)
	if err := f.ctl.Close(); err != nil {
		t.Fatal(err)
	}

	fresh := openController(t, f.env)
	if err := fresh.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := fresh.Overview(ctx, OverviewQuery{Cwd: f.repo.Root})
	if err != nil {
		t.Fatal(err)
	}
	strip := func(ov Overview) []TaskView {
		var out []TaskView
		for _, r := range ov.Rows {
			r.Age = 0
			out = append(out, r)
		}
		return out
	}
	if len(after.Rows) != 2 || !reflect.DeepEqual(strip(before), strip(after)) {
		t.Fatalf("recovered rows differ:\n%+v\n%+v", strip(before), strip(after))
	}
	if snapAfter := f.srv.Snapshot(t); !reflect.DeepEqual(snapBefore, snapAfter) {
		t.Fatalf("tmux changed across the restart:\n%+v\n%+v", snapBefore, snapAfter)
	}
	for _, r := range after.Rows {
		if l, _ := fresh.liveSession(ctx, piSession(t, fresh, r.ID)); !l.IsAlive() {
			t.Fatalf("%s not alive after recovery: %v", r.ID, l)
		}
	}
}
