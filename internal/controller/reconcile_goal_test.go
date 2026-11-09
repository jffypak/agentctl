package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

// TestReconcileStartingTimeoutAfterSessionStartIsGoalNeverSubmitted
// (m3-goal-start-race): a launched-with-goal agent that sent SessionStart
// (starting, confidence hook, "submitting goal") but never its goal's
// UserPromptSubmit still becomes unknown on StartingTimeout. Its detail says
// the goal was never submitted, not the folder-trust hint (a trust dialog
// comes before any hook).
func TestReconcileStartingTimeoutAfterSessionStartIsGoalNeverSubmitted(t *testing.T) {
	t.Parallel()
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	env := agentEnv(srv.Env, sleepAgent)
	repo := testenv.TempRepo(t, env)
	clock := &piClock{t: time.Now()}
	ctl := piOpen(t, env, Tunables{StartingTimeout: 2 * time.Second}, clock.Now)
	sess := piNewTask(t, ctl, repo, "demo")
	// what hookingest records for SessionStart(startup) on a task with a goal
	if _, err := ctl.store.Sessions().Transition(context.Background(), sess.ID, domain.StateStarting,
		domain.ConfidenceHook, "submitting goal", domain.SourceHook); err != nil {
		t.Fatal(err)
	}

	clock.Advance(2 * time.Second)
	piReconcile(t, ctl)
	s := piSession(t, ctl, "demo")
	if s.State != domain.StateUnknown || s.Confidence != domain.ConfidenceNone {
		t.Fatalf("after the timeout: %+v", s)
	}
	if !strings.HasPrefix(s.LastDetail, "goal never submitted") || strings.Contains(s.LastDetail, "folder-trust") ||
		!strings.Contains(s.LastDetail, "attach to check") {
		t.Fatalf("detail = %q", s.LastDetail)
	}
	if a := domain.ClassifyAttention(domain.AttentionInput{TaskState: domain.TaskRunning, HasSession: true,
		AgentState: s.State, Origin: s.Origin}); a.Section != domain.SectionNeedsYou {
		t.Fatalf("attention = %+v, want NEEDS YOU", a)
	}
}
