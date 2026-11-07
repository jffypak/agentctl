package claude

import (
	"context"
	"errors"
	"strings"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Observe probes the handle's own socket (one snapshot: server pid + every
// pane) and classifies liveness with domain.ClassifyLiveness against
// h.Identity. Probe failures are expressed in the verdict, not the error:
// "no server" means server_gone; any other failure (tmux missing, exec
// error, context cancelled) yields domain.UnverifiedLiveness, never
// disconnected, because disconnected is sticky. The error return is reserved
// for future handle validation and is currently always nil.
func (a *Adapter) Observe(ctx context.Context, h adapter.Handle) (adapter.Observation, error) {
	id := h.Identity
	if id.IsZero() {
		return adapter.Observation{Liveness: domain.ClassifyLiveness(id, domain.PaneObservation{})}, nil
	}
	snap, err := a.client(id.Socket).Snapshot(ctx)
	switch {
	case errors.Is(err, tmux.ErrNoServer):
		obs := domain.PaneObservation{ServerReachable: false}
		return adapter.Observation{Liveness: domain.ClassifyLiveness(id, obs), Pane: obs}, nil
	case err != nil:
		return adapter.Observation{Liveness: domain.UnverifiedLiveness(oneLine(err.Error()))}, nil
	}
	obs := snap.Observe(id.PaneID)
	return adapter.Observation{Liveness: domain.ClassifyLiveness(id, obs), Pane: obs}, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
