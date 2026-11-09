package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// DrainResult reports one Drain pass for a session.
type DrainResult struct {
	MessageID int64  // the message claimed (0 if none)
	Delivered bool   // a paste happened and the message is sent
	Reason    string // why nothing was delivered ("agent working", "awaiting input", ...)
}

// Reasons Drain reports besides the domain delivery labels.
const (
	ReasonNothingQueued = "nothing queued"
	ReasonOnePerTurn    = "one message per turn — waits for the next Stop"
	ReasonStateChanged  = "state changed before the paste"
	ReasonTaskClosed    = "task closed"
)

// DrainStage names a fault seam inside Drain (tests simulate a crash there).
type DrainStage string

const (
	DrainAfterClaim DrainStage = "after_claim" // claimed, nothing pasted yet
	DrainAfterPaste DrainStage = "after_paste" // pasted + Enter, not yet marked sent
)

// DrainFault, when it returns an error at a stage, makes Drain stop right
// there and return that error, leaving the message as a crash would.
type DrainFault func(stage DrainStage, messageID int64) error

var drainFaults sync.Map // *Controller -> DrainFault

// InjectDrainFault installs f for this controller (nil removes it). Tests
// only: it simulates agentctl dying between the steps of a delivery.
func (c *Controller) InjectDrainFault(f DrainFault) {
	if f == nil {
		drainFaults.Delete(c)
		return
	}
	drainFaults.Store(c, f)
}

func (c *Controller) fault(stage DrainStage, id int64) error {
	if f, ok := drainFaults.Load(c); ok {
		return f.(DrainFault)(stage, id)
	}
	return nil
}

// Drain is the idempotent delivery step shared by the TUI tick, `send`, and
// `agentctl deliver` (ARCHITECTURE.md §Delivery):
//
//  1. sweep this session's stale delivering / sent messages to unconfirmed
//     (a crashed deliverer; never redelivered, invariant 4);
//  2. stored state must allow a delivery (domain.QueueLabel, including the
//     lost-hook marker check), else report why and touch nothing;
//  3. claim the oldest queued message with one conditional UPDATE
//     (store.Messages.Claim: ready + hook + one per ready transition);
//  4. re-verify from the registry and the live pane (liveSession): still
//     ready, hook, same state_since as at the claim, not stale, alive;
//     otherwise revert to queued, nothing pasted;
//  5. paste + Enter through the adapter (identity guarded again in tmux);
//     a refused paste reverts to queued, any uncertain outcome becomes
//     unconfirmed;
//  6. mark sent (cause_id = the hook that made the session ready), then
//     acknowledge at once if the UserPromptSubmit already raced ahead.
func (c *Controller) Drain(ctx context.Context, sessionID int64) (DrainResult, error) {
	msgs := c.store.Messages()
	if _, err := msgs.SweepStale(ctx, sessionID, c.tun.AckWindow); err != nil {
		return DrainResult{}, err
	}
	sess, err := c.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return DrainResult{}, err
	}
	if task, err := c.store.Tasks().Get(ctx, sess.TaskID); err != nil {
		return DrainResult{}, err
	} else if task.State == domain.TaskClosed {
		return DrainResult{Reason: ReasonTaskClosed}, nil
	}
	if label := domain.QueueLabel(sess.State, sess.Confidence, c.staleHook(sess)); label != "" {
		return DrainResult{Reason: label}, nil
	}
	cause, err := msgs.ReadyCause(ctx, sessionID)
	if err != nil {
		return DrainResult{}, err
	}
	before, err := c.store.Events().LastID(ctx)
	if err != nil {
		return DrainResult{}, err
	}
	claim, ok, err := msgs.Claim(ctx, sessionID, cause)
	if err != nil {
		return DrainResult{}, err
	}
	if !ok {
		queued, err := msgs.HasQueued(ctx, sessionID)
		if err != nil {
			return DrainResult{}, err
		}
		if queued {
			return DrainResult{Reason: ReasonOnePerTurn}, nil
		}
		return DrainResult{Reason: ReasonNothingQueued}, nil
	}
	m := claim.Message
	res := DrainResult{MessageID: m.ID}
	if err := c.fault(DrainAfterClaim, m.ID); err != nil {
		return res, err
	}
	cur, err := c.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return res, errors.Join(err, c.revert(ctx, m.ID))
	}
	live, _ := c.liveSession(ctx, cur)
	ok, label := domain.CanDeliver(domain.DeliveryCheck{
		State: cur.State, Confidence: cur.Confidence, Liveness: live.Status, StaleHook: c.staleHook(cur),
	})
	if ok && !cur.StateSince.Equal(claim.StateSince) {
		ok, label = false, ReasonStateChanged
	}
	if !ok {
		res.Reason = label
		return res, c.revert(ctx, m.ID)
	}
	h := adapter.Handle{Identity: cur.Identity, SessionName: cur.SessionName}
	dctx := claude.WithPasteEnterDelay(ctx, c.tun.PasteEnterDelay)
	rc, err := c.agent.Deliver(dctx, h, adapter.Message{ID: m.ID, Body: m.Body, BodySHA256: m.BodySHA256})
	if errors.Is(err, adapter.ErrNotReady) {
		res.Reason = "pane refused the paste: " + OneLine(err.Error())
		return res, c.revert(ctx, m.ID)
	}
	if err != nil {
		claimedEv, _ := msgs.MessageEventID(context.WithoutCancel(ctx), domain.EventMessageClaimed, m.ID)
		_, uerr := msgs.MarkUnconfirmed(context.WithoutCancel(ctx), m.ID, "delivery failed: "+OneLine(err.Error()), claimedEv)
		res.Reason = "delivery failed"
		return res, errors.Join(fmt.Errorf("deliver message %d: %w", m.ID, err), uerr)
	}
	if err := c.fault(DrainAfterPaste, m.ID); err != nil {
		return res, err
	}
	if _, err := msgs.MarkSent(context.WithoutCancel(ctx), m.ID, rc.SentAt, cause); err != nil {
		return res, err
	}
	res.Delivered = true
	if _, err := c.ackFromSubmits(context.WithoutCancel(ctx), sessionID, before); err != nil {
		return res, err
	}
	return res, nil
}

// revert returns a claimed message to the queue (nothing was pasted).
func (c *Controller) revert(ctx context.Context, id int64) error {
	return c.store.Messages().Revert(context.WithoutCancel(ctx), id)
}

// ackFromSubmits applies every UserPromptSubmit recorded for the session
// after event afterID (normally the ack followup in the hook does this; the
// submit's hook can win the race against MarkSent). It returns the decision
// of the first submit that acknowledged something.
func (c *Controller) ackFromSubmits(ctx context.Context, sessionID, afterID int64) (domain.AckDecision, error) {
	msgs := c.store.Messages()
	subs, err := msgs.SubmitsSince(ctx, sessionID, afterID)
	if err != nil {
		return domain.AckDecision{}, err
	}
	for _, s := range subs {
		d, err := msgs.AckFromHook(ctx, sessionID, s.PromptSHA256, s.TS, c.tun.AckWindow, s.EventID)
		if err != nil || d.MessageID != 0 {
			return d, err
		}
	}
	return domain.AckDecision{}, nil
}

// DrainAll sweeps stale deliveries everywhere, then runs Drain for every
// session with queued messages (the TUI's reconcile tick).
func (c *Controller) DrainAll(ctx context.Context) error {
	msgs := c.store.Messages()
	if _, err := msgs.SweepStale(ctx, 0, c.tun.AckWindow); err != nil {
		return err
	}
	ids, err := msgs.QueuedSessions(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if _, err := c.Drain(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("drain session %d: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// --- lost-hook safety (ARCHITECTURE.md §Lost-hook safety) -----------------

// LostHookDir is where `agentctl hook` writes a marker when it cannot record
// an event (hookingest/lost.go writes, this file reads).
func LostHookDir(hooksDir string) string { return filepath.Join(hooksDir, "lost") }

// lostMarker is the marker file's content.
type lostMarker struct {
	TSMillis int64 `json:"ts_ms"`
}

// lostHookAt is the newest lost-hook marker time that can refer to sess
// (by session, task, or pane key); ok false when there is none.
func (c *Controller) lostHookAt(sess store.Session) (time.Time, bool) {
	dir := LostHookDir(c.paths.Hooks)
	var newest time.Time
	for _, key := range domain.LostHookKeys(sess.ID, sess.TaskID, sess.Identity.PaneID) {
		p := filepath.Join(dir, key)
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		at := fi.ModTime()
		if b, err := os.ReadFile(p); err == nil {
			var m lostMarker
			if json.Unmarshal(b, &m) == nil && m.TSMillis > 0 {
				at = time.UnixMilli(m.TSMillis)
			}
		}
		if at.After(newest) {
			newest = at
		}
	}
	return newest, !newest.IsZero()
}

// staleHook reports whether a lost-hook marker is newer than the session's
// state: its recorded state can no longer be trusted (a lost
// UserPromptSubmit would leave it "ready" while the agent works).
func (c *Controller) staleHook(sess store.Session) bool {
	at, ok := c.lostHookAt(sess)
	return ok && at.After(sess.StateSince)
}

// downgradeLostHooks is reconcile's lost-hook step: every live session whose
// marker is newer than its state becomes unknown / none (source lost_hook).
// The next recorded hook for it moves it on and clears the marker.
func (c *Controller) downgradeLostHooks(ctx context.Context, active []store.Session) error {
	var errs []error
	for _, sess := range active {
		if sess.State.IsTerminal() || sess.State == domain.StateUnknown || !c.staleHook(sess) {
			continue
		}
		_, err := c.store.Sessions().ReconcileTransition(ctx, store.ReconcileInput{
			SessionID: sess.ID, From: sess.State, To: domain.StateUnknown, Confidence: domain.ConfidenceNone,
			Detail: "a hook event could not be recorded; " + domain.LabelStale, Source: domain.SourceLostHook,
		})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
