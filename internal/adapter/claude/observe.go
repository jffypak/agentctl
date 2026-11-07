package claude

import (
	"context"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
)

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-pane-identity.

// Observe probes the handle's socket (one snapshot) and classifies liveness
// with domain.ClassifyLiveness against h.Identity. A probe failure that is
// not "no server" must yield domain.UnverifiedLiveness, never disconnected.
func (a *Adapter) Observe(ctx context.Context, h adapter.Handle) (adapter.Observation, error) {
	return adapter.Observation{Liveness: domain.UnverifiedLiveness("observe not implemented")}, adapter.ErrNotImplemented
}
