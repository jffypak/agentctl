package claude

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/tmux"
)

// DefaultPasteEnterDelay is the pause between the paste and the Enter that
// submits it (controller.Tunables.PasteEnterDelay), so the agent has drawn
// the pasted text before the key arrives.
const DefaultPasteEnterDelay = 100 * time.Millisecond

type pasteEnterDelayKey struct{}

// WithPasteEnterDelay returns a context carrying the paste→Enter delay
// Deliver uses (the AgentAdapter signature has no room for it; the
// controller sets it from its Tunables).
func WithPasteEnterDelay(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, pasteEnterDelayKey{}, d)
}

func pasteEnterDelay(ctx context.Context) time.Duration {
	if d, ok := ctx.Value(pasteEnterDelayKey{}).(time.Duration); ok && d >= 0 {
		return d
	}
	return DefaultPasteEnterDelay
}

// ErrNotSubmitted: the text was pasted but the Enter could not be sent (or
// may not have been): the message must become unconfirmed, never re-pasted.
var ErrNotSubmitted = errors.New("pasted but not submitted")

// Deliver pastes m into the handle's pane and submits it (Delivery rule 3):
//
//	load-buffer -b agentctl-msg-<id> -   (body on stdin)
//	paste-buffer -p -d -b … -t <pane>    (bracketed paste, buffer deleted)
//	PasteEnterDelay
//	send-keys -t <pane> Enter
//
// The caller (controller.Drain) has claimed the message and re-verified
// state and identity. Both writes are guarded again inside the tmux server by
// the identity triple (and the pane being alive, out of copy mode and
// accepting input), so a %N reused in between never receives anything.
//
// Error contract: an error wrapping adapter.ErrNotReady means nothing was
// pasted (the caller may re-queue). Any other error means the paste may have
// happened (ErrNotSubmitted: it did, the Enter did not): the caller must
// treat the message as unconfirmed and never paste it again.
func (a *Adapter) Deliver(ctx context.Context, h adapter.Handle, m adapter.Message) (adapter.Receipt, error) {
	id := h.Identity
	if id.IsZero() || id.Socket == "" {
		return adapter.Receipt{}, fmt.Errorf("claude: deliver message %d: no pane identity: %w", m.ID, adapter.ErrNotReady)
	}
	target := tmux.WriteTarget{PaneID: id.PaneID, ServerPID: id.ServerPID, PanePID: id.PanePID}
	buffer := tmux.BufferName(m.ID)
	c := a.client(id.Socket)
	if err := c.LoadAndPaste(ctx, target, buffer, m.Body); err != nil {
		if errors.Is(err, tmux.ErrPasteRefused) {
			return adapter.Receipt{}, fmt.Errorf("claude: deliver message %d to %s: %v: %w", m.ID, id.PaneID, err, adapter.ErrNotReady)
		}
		// unknown outcome: never leave the text in a tmux buffer
		_ = c.DeleteBuffer(context.WithoutCancel(ctx), buffer)
		return adapter.Receipt{}, fmt.Errorf("claude: deliver message %d to %s: %w", m.ID, id.PaneID, err)
	}
	if err := sleep(ctx, pasteEnterDelay(ctx)); err != nil {
		return adapter.Receipt{}, fmt.Errorf("claude: deliver message %d: %w: %v", m.ID, ErrNotSubmitted, err)
	}
	if err := c.SendEnter(ctx, target); err != nil {
		return adapter.Receipt{}, fmt.Errorf("claude: deliver message %d to %s: %w: %v", m.ID, id.PaneID, ErrNotSubmitted, err)
	}
	return adapter.Receipt{SentAt: a.cfg.Now(), Buffer: buffer}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
