package hookingest

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// countSpawns makes the run record (not perform) deliver spawns.
func countSpawns(n *atomic.Int32) func(*Config) {
	return func(c *Config) {
		c.Spawner = SpawnerFunc(func(int64, int) error { n.Add(1); return nil })
	}
}

// TestSessionStartWithGoalStaysStarting (m3-goal-start-race): with a goal on
// argv, SessionStart(startup) fires before the goal's UserPromptSubmit. The
// agent must not look deliverable in between: it stays starting ("submitting
// goal", now hook-confident) and no deliver is spawned for a queued message;
// the goal turn's Stop is the first ready. The goal comes from the registry,
// so a fresh hook process (as every hook is) gets it right.
func TestSessionStartWithGoalStaysStarting(t *testing.T) {
	f := newGoalFixture(t, domain.OriginLaunched, domain.StateStarting, "write the tests")
	ctx := context.Background()
	if _, err := f.st.Messages().Enqueue(ctx, store.NewMessage{TaskID: f.sess.TaskID, SessionID: f.sess.ID, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	var spawns atomic.Int32

	r := f.run(f.env("demo"), payload("SessionStart", "source", "startup"), countSpawns(&spawns))
	if r.Err != nil || r.Orphan != "" {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	s := f.session()
	if s.State != domain.StateStarting || s.Confidence != domain.ConfidenceHook || s.LastDetail != claude.DetailSubmittingGoal {
		t.Fatalf("after SessionStart: %+v", s)
	}
	if ch := f.events(domain.EventAgentStateChanged); len(ch) != 0 {
		t.Fatalf("SessionStart changed the state: %+v", ch)
	}
	if n := spawns.Load(); n != 0 {
		t.Fatalf("SessionStart spawned %d deliver(s) before the goal turn", n)
	}

	for _, step := range []struct {
		event  string
		kv     []string
		want   domain.AgentState
		spawns int32
	}{
		{"UserPromptSubmit", []string{"prompt", "write the tests"}, domain.StateWorking, 0},
		{"Stop", nil, domain.StateReady, 1},
	} {
		if r := f.run(f.env("demo"), payload(step.event, step.kv...), countSpawns(&spawns)); r.Err != nil {
			t.Fatalf("%s: %+v", step.event, r.Outcome)
		}
		if got := f.session().State; got != step.want || spawns.Load() != step.spawns {
			t.Fatalf("after %s: %s with %d spawns, want %s with %d", step.event, got, spawns.Load(), step.want, step.spawns)
		}
	}
	if f.errorLog() != "" {
		t.Fatalf("error log: %q", f.errorLog())
	}
}

// TestSessionStartReadyWithoutPendingGoal: no goal, an adopted session, or a
// SessionStart that is not the launch (resume, clear) still means ready.
func TestSessionStartReadyWithoutPendingGoal(t *testing.T) {
	cases := []struct {
		name   string
		origin domain.Origin
		goal   string
		task   string // AGENTCTL_TASK_ID ("" = adopted pane, matched by identity)
		source string
	}{
		{"launched without goal", domain.OriginLaunched, "", "demo", "startup"},
		{"adopted", domain.OriginAdopted, "a goal", "", "startup"},
		{"resume", domain.OriginLaunched, "a goal", "demo", "resume"},
		{"clear", domain.OriginLaunched, "a goal", "demo", "clear"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newGoalFixture(t, c.origin, domain.StateStarting, c.goal)
			r := f.run(f.env(c.task), payload("SessionStart", "source", c.source))
			if r.Err != nil || r.Orphan != "" || !r.Applied {
				t.Fatalf("outcome = %+v", r.Outcome)
			}
			if s := f.session(); s.State != domain.StateReady || s.LastDetail != "session started ("+c.source+")" {
				t.Fatalf("session = %+v", s)
			}
		})
	}
}

// TestSessionStartWithGoalAfterTimeoutStaysUnknown: an agent whose
// SessionStart came after StartingTimeout (a slow folder-trust answer) is
// unknown; the goal hold never moves it back to starting, and never to ready.
func TestSessionStartWithGoalAfterTimeoutStaysUnknown(t *testing.T) {
	f := newGoalFixture(t, domain.OriginLaunched, domain.StateUnknown, "a goal")
	r := f.run(f.env("demo"), payload("SessionStart", "source", "startup"))
	if r.Err != nil || r.Applied {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	if s := f.session(); s.State != domain.StateUnknown {
		t.Fatalf("session = %+v", s)
	}
	if r := f.run(f.env("demo"), payload("UserPromptSubmit", "prompt", "a goal")); r.Err != nil || f.session().State != domain.StateWorking {
		t.Fatalf("goal submit after timeout: %+v / %s", r.Outcome, f.session().State)
	}
	if f.errorLog() != "" {
		t.Fatalf("error log: %q", f.errorLog())
	}
}
