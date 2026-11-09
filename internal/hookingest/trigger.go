package hookingest

import (
	"context"

	"github.com/jffypak/agentctl/internal/domain"
)

var _ = registerFollowup(20, "trigger", triggerFollowup)

// triggerFollowup starts background delivery: when this hook moved the
// session to ready (Stop, Notification idle_prompt, SessionStart) with hook
// confidence and messages are queued, it spawns a detached `agentctl
// deliver --session N --after-pid <this hook>`. The spawn returns at once;
// deliver waits for this process to exit (its state write is committed),
// then drains. It never pastes anything itself.
func triggerFollowup(ctx context.Context, d Deps, a Applied) error {
	if !a.Result.Transition.Changed || a.Session.State != domain.StateReady || a.Session.Confidence != domain.ConfidenceHook {
		return nil
	}
	queued, err := d.Store.Messages().HasQueued(ctx, a.Session.ID)
	if err != nil || !queued {
		return err
	}
	return d.Spawner.SpawnDeliver(a.Session.ID, d.PID)
}
