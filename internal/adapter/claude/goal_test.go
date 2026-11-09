package claude_test

import (
	"testing"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
)

// TestHoldForGoal: a launched-with-goal session's SessionStart(startup) keeps
// the agent starting ("submitting goal"); every other observation is
// returned unchanged.
func TestHoldForGoal(t *testing.T) {
	a := claude.New(claude.Config{})
	parse := func(raw string) adapter.HookObservation {
		t.Helper()
		obs, err := a.ParseHook([]byte(raw), adapter.HookEnv{})
		if err != nil {
			t.Fatal(err)
		}
		return obs
	}

	start := parse(`{"hook_event_name":"SessionStart","source":"startup","session_id":"s"}`)
	held, ok := claude.HoldForGoal(start)
	if !ok || held.State != domain.StateStarting || held.Detail != claude.DetailSubmittingGoal || held.Event != "SessionStart" {
		t.Fatalf("held = %+v, %v", held, ok)
	}
	if start.State != domain.StateReady {
		t.Fatalf("HoldForGoal mutated its input: %+v", start)
	}
	if held.Attrs["source"] != "startup" || held.AgentSessionID != "s" {
		t.Fatalf("held lost fields: %+v", held)
	}

	for _, raw := range []string{
		`{"hook_event_name":"SessionStart","source":"resume"}`,
		`{"hook_event_name":"SessionStart","source":"clear"}`,
		`{"hook_event_name":"SessionStart","source":"compact"}`,
		`{"hook_event_name":"SessionStart"}`,
		`{"hook_event_name":"UserPromptSubmit","prompt":"go"}`,
		`{"hook_event_name":"Stop"}`,
	} {
		obs := parse(raw)
		got, ok := claude.HoldForGoal(obs)
		if ok || got.State != obs.State || got.Detail != obs.Detail {
			t.Errorf("%s: got %+v, %v; want unchanged %+v", raw, got, ok, obs)
		}
	}
}
