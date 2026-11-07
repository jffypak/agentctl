package claude

import (
	"context"

	"github.com/jffypak/agentctl/internal/adapter"
)

// STUB (m1-walking-skeleton) — owned and rewritten whole by m2-prompt-queue.

// Deliver pastes m into the verified pane: load-buffer -b agentctl-msg-<id> -
// (stdin), paste-buffer -p -d -t <pane>, PasteEnterDelay, send-keys Enter.
// It is the only caller of the tmux paste/send-keys builders (archtest).
// The caller (controller.Drain) has already claimed the message and
// re-verified state and identity; Deliver returns adapter.ErrNotReady if its
// own final identity check fails.
func (a *Adapter) Deliver(ctx context.Context, h adapter.Handle, m adapter.Message) (adapter.Receipt, error) {
	return adapter.Receipt{}, adapter.ErrNotImplemented
}
