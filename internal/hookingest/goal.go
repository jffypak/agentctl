package hookingest

import (
	"context"
	"fmt"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// withGoalHold applies claude.HoldForGoal when the matched session was
// launched by agentctl with a goal on argv (m3-goal-start-race). Whether
// there was a goal is read from the registry's task row, never from process
// state, so every hook process (and a restarted controller) agrees. Adopted
// sessions and tasks without a goal keep SessionStart -> ready. The task is
// read only for a SessionStart(startup), so other hooks pay nothing.
func withGoalHold(ctx context.Context, st *store.Store, sess store.Session, obs adapter.HookObservation) (adapter.HookObservation, error) {
	held, ok := claude.HoldForGoal(obs)
	if !ok || sess.Origin != domain.OriginLaunched {
		return obs, nil
	}
	task, err := st.Tasks().Get(ctx, sess.TaskID)
	if err != nil {
		return obs, fmt.Errorf("read task %s for its goal: %w", sess.TaskID, err)
	}
	if task.Goal == "" {
		return obs, nil
	}
	return held, nil
}
