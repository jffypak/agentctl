package claude

import (
	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
)

// DetailSubmittingGoal is the detail of a launched-with-goal agent between
// its SessionStart and its goal's UserPromptSubmit.
const DetailSubmittingGoal = "submitting goal"

// HoldForGoal adjusts the observation of a session that was launched with a
// goal on argv (m3-goal-start-race). Claude Code fires SessionStart(startup)
// ~230 ms before the goal's UserPromptSubmit; mapping it to ready would let a
// send paste into an agent about to start working. Such a SessionStart keeps
// the agent starting with DetailSubmittingGoal instead, so the goal turn's
// Stop is its first ready and StartingTimeout stays the lost-hook backstop.
// Every other observation is returned unchanged with ok == false. Whether
// the session has a pending goal is the caller's (registry) knowledge. Pure.
func HoldForGoal(obs adapter.HookObservation) (held adapter.HookObservation, ok bool) {
	if obs.Event != "SessionStart" || obs.Attrs["source"] != "startup" {
		return obs, false
	}
	held = obs
	held.State, held.Detail = domain.StateStarting, DetailSubmittingGoal
	return held, true
}
