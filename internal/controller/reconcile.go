package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Reconcile verifies every active session (non-terminal sessions of running
// tasks) against tmux and records what it proves:
//
//   - one c.snapshot per socket (display-message pid + list-panes -a, a single
//     tmux invocation), then livenessFrom per session;
//   - dead pane -> stopped ("exit 0") or failed ("exit N" / "signal N");
//     missing pane, other server pid or other pane pid -> disconnected with
//     session.disconnected{reason}; all with source pane, confidence pane;
//   - a live session with a lost-hook marker newer than its state -> unknown
//     / none, source lost_hook (m2-prompt-queue: downgradeLostHooks);
//   - starting with no hook yet whose live pane shows Claude's folder-trust
//     dialog -> unknown / none, detail domain.FolderTrustDetail, source
//     pane_text, on the first poll (m3-folder-trust-detect, foldertrust.go;
//     guarded like the timeout). Only these sessions are captured: the
//     bottom of the visible screen, once per poll;
//   - starting with no hook for Tunables.StartingTimeout -> unknown / none,
//     source timeout (guarded: a hook that arrives first wins);
//   - LivenessUnverified (probe failed) is never treated as a pane verdict;
//     the probe error is returned so `ls` can warn. The starting timeout is
//     about missing hooks, not the pane, so it still applies (unknown is the
//     honest label when nothing can be verified, invariant 6).
//
// It is idempotent and safe to run concurrently (store.ReconcileTransition
// re-reads the state inside its transaction), so `ls` and the TUI tick can
// both call it. Terminal states are never left. A reused %N is never bound
// to anything: reconcile only ever compares stored identities.
func (c *Controller) Reconcile(ctx context.Context) error {
	active, err := c.store.Sessions().ListActive(ctx)
	if err != nil {
		return err
	}
	var (
		order    []string
		bySocket = map[string][]store.Session{}
		errs     []error
	)
	errs = append(errs, c.downgradeLostHooks(ctx, active)) // lost-hook safety (m2-prompt-queue, drain.go)
	for _, sess := range active {
		if sess.Identity.IsZero() {
			l, _ := livenessFrom(sess.Identity, tmux.Snapshot{}, nil)
			errs = append(errs, c.reconcileSession(ctx, sess, l))
			continue
		}
		sock := sess.Identity.Socket
		if _, ok := bySocket[sock]; !ok {
			order = append(order, sock)
		}
		bySocket[sock] = append(bySocket[sock], sess)
	}
	for _, sock := range order {
		snap, perr := c.snapshot(ctx, sock)
		if perr != nil && !errors.Is(perr, tmux.ErrNoServer) {
			errs = append(errs, fmt.Errorf("cannot probe tmux on %s: %w", sock, perr))
		}
		for _, sess := range bySocket[sock] {
			l, _ := livenessFrom(sess.Identity, snap, perr)
			errs = append(errs, c.reconcileSession(ctx, sess, l))
		}
	}
	return errors.Join(errs...)
}

// reconcileSession records one verdict. It is also used by close --stop for
// a dead pane, so the exit status is kept rather than "stopped by user".
func (c *Controller) reconcileSession(ctx context.Context, sess store.Session, l domain.Liveness) error {
	if to, detail, ok := l.ImpliedState(); ok {
		_, err := c.store.Sessions().ReconcileTransition(ctx, store.ReconcileInput{
			SessionID: sess.ID, To: to, Confidence: domain.ConfidencePane, Detail: detail,
			Source: domain.SourcePane, Reason: l.Reason,
		})
		return err
	}
	if awaitingFirstHook(sess) && l.IsAlive() && c.showsFolderTrust(ctx, sess) {
		return c.markFolderTrust(ctx, sess) // before the timeout: the specific detail wins
	}
	if sess.State == domain.StateStarting && !c.now().Before(sess.StateSince.Add(c.tun.StartingTimeout)) {
		_, err := c.store.Sessions().ReconcileTransition(ctx, store.ReconcileInput{
			SessionID: sess.ID, From: domain.StateStarting, To: domain.StateUnknown, Confidence: domain.ConfidenceNone,
			Detail: "no hook within " + c.tun.StartingTimeout.Round(time.Millisecond).String() +
				" — Claude may be showing its folder-trust or login prompt; attach to check",
			Source: domain.SourceTimeout,
		})
		return err
	}
	return nil
}
