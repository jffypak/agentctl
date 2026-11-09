package hookingest

import (
	"context"
	"time"
)

// AckWindow is controller.Tunables.AckWindow: a UserPromptSubmit within this
// long of a paste acknowledges it by timing when the prompt hash does not
// match (hookingest keeps its own copy because it must not import
// controller).
const AckWindow = 15 * time.Second

// eventUserPromptSubmit is the Claude Code hook that acknowledges a paste.
const eventUserPromptSubmit = "UserPromptSubmit"

var _ = registerFollowup(10, "ack", ackFollowup)

// ackFollowup is Delivery rule 4: on UserPromptSubmit, a pasted message of
// this session whose body hash equals the prompt hash becomes acknowledged
// (ack_match=hash; also a late match on an unconfirmed one); otherwise the
// first submit within AckWindow of a paste acknowledges it by timing. The
// message.acknowledged event's cause_id is this hook's hook.received.
func ackFollowup(ctx context.Context, d Deps, a Applied) error {
	if a.Observation.Event != eventUserPromptSubmit || a.Session.ID == 0 {
		return nil
	}
	_, err := d.Store.Messages().AckFromHook(ctx, a.Session.ID, a.Observation.PromptSHA256, d.Now(), AckWindow, a.Result.ReceivedID)
	return err
}
