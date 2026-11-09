package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

// goalGapMS is fakeagent's SessionStart -> goal submit gap for the race test.
// Real Claude Code showed ~230 ms; a wide gap lets the test land its send
// inside it even on a loaded machine (it asserts that it did).
const goalGapMS = "4000"

// TestGoalStartRaceQueuesSend (m3-goal-start-race, invariant 3): a task
// launched with a goal fires SessionStart well before its goal's
// UserPromptSubmit. A send in that gap is queued ("agent starting"), never
// pasted; it is delivered after the goal turn's Stop, and the agent's first
// ready is that Stop, not SessionStart.
func TestGoalStartRaceQueuesSend(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_GOAL_DELAY_MS="+goalGapMS, "FAKEAGENT_WORK_MS=300")
	r.newAgent("alpha", "outline the notes")

	testenv.WaitFor(t, stateWait, "alpha's SessionStart recorded", func() (bool, string) {
		tk := r.task("alpha")
		return tk.Confidence == "hook", tk.AgentState + " / " + tk.Confidence + " / " + tk.LastDetail
	})
	if tk := r.task("alpha"); tk.AgentState != "starting" || tk.LastDetail != "submitting goal" {
		t.Fatalf("after SessionStart: %s %q, want starting \"submitting goal\"", tk.AgentState, tk.LastDetail)
	}
	out, errOut, code := r.agentctl("send", "alpha", "hello from the gap")
	if code != 0 || !strings.Contains(out, "queued") || !strings.Contains(out, domain.LabelStarting) {
		t.Fatalf("send in the gap: exit %d %q %s (want queued · %s: sent inside the gap)", code, out, errOut, domain.LabelStarting)
	}
	if p := r.pastes("alpha"); len(p) != 0 {
		t.Fatalf("pasted during the goal-start gap: %+v", p)
	}

	r.waitMessage("alpha", domain.MsgAcknowledged)
	r.waitState("alpha", "ready")
	r.assertNothingWhileBusy("alpha")
	if p := r.pastes("alpha"); len(p) != 1 || p[0].Text != "hello from the gap" || p[0].State != "ready" {
		t.Fatalf("pastes = %+v, want exactly the queued message, received while ready", p)
	}
	changes := r.stateChanges("alpha")
	if slices.Contains(changes, "starting>ready") || len(changes) == 0 || changes[0] != "starting>working" {
		t.Fatalf("state changes = %v, want starting>working first and no starting>ready", changes)
	}
}

// TestNoGoalStillReadyOnSessionStart: without a goal there is no goal turn to
// wait for; SessionStart makes the agent ready, as before.
func TestNoGoalStillReadyOnSessionStart(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_GOAL_DELAY_MS="+goalGapMS)
	r.newAgent("idle", "")
	tk := r.waitState("idle", "ready")
	if tk.LastDetail != "session started (startup)" {
		t.Fatalf("detail = %q", tk.LastDetail)
	}
	if got := r.stateChanges("idle"); !slices.Equal(got, []string{"starting>ready"}) {
		t.Fatalf("state changes = %v", got)
	}
}
