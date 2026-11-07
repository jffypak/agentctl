package controller

import (
	"context"
	"errors"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/tmux"
)

// liveSession is the one identity check that guards capture, attach and
// delivery (invariant 5). It probes the session's own socket and compares
// (server pid, pane id, pane pid) with the stored identity. The pane is
// returned only when the identity is verified (alive or dead), so a reused %N
// can never leak another agent's output or focus. A probe that fails for any
// reason other than "no server" yields LivenessUnverified, which callers must
// treat as "refuse, change nothing".
func (c *Controller) liveSession(ctx context.Context, sess store.Session) (domain.Liveness, tmux.Pane) {
	if sess.Identity.IsZero() {
		return domain.ClassifyLiveness(sess.Identity, domain.PaneObservation{}), tmux.Pane{}
	}
	snap, err := c.snapshot(ctx, sess.Identity.Socket)
	return livenessFrom(sess.Identity, snap, err)
}

// snapshot probes one socket (server pid + every pane, one tmux call).
// Reconcile groups sessions by socket and calls this once per socket.
func (c *Controller) snapshot(ctx context.Context, socket string) (tmux.Snapshot, error) {
	return c.tmuxClient(socket).Snapshot(ctx)
}

// livenessFrom classifies one identity against a snapshot result.
func livenessFrom(id domain.Identity, snap tmux.Snapshot, err error) (domain.Liveness, tmux.Pane) {
	switch {
	case id.IsZero():
		return domain.ClassifyLiveness(id, domain.PaneObservation{}), tmux.Pane{}
	case errors.Is(err, tmux.ErrNoServer):
		return domain.ClassifyLiveness(id, domain.PaneObservation{ServerReachable: false}), tmux.Pane{}
	case err != nil:
		return domain.UnverifiedLiveness(OneLine(err.Error())), tmux.Pane{}
	}
	l := domain.ClassifyLiveness(id, snap.Observe(id.PaneID))
	if !l.IsVerified() {
		return l, tmux.Pane{}
	}
	p, _ := snap.Find(id.PaneID)
	return l, p
}
