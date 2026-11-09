package controller

import (
	"context"
	"errors"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// Send errors (errors.Is).
var (
	ErrInvalidMessage = errors.New("invalid message")
	ErrSendRejected   = errors.New("send rejected")
)

// Send sources recorded in message.queued.
const (
	SendSourceCLI = "cli"
	SendSourceTUI = "tui"
)

// SendRequest is one prompt for a task's agent.
type SendRequest struct {
	TaskID string
	Body   string
	Source string // SendSourceCLI / SendSourceTUI
}

// SendResult is where the prompt ended up after the immediate drain.
type SendResult struct {
	Message   MessageView // its state and label after the drain
	Delivered bool        // pasted now (sent)
	DrainErr  string      // the immediate delivery attempt failed (the message is kept)
}

// Send validates the body (non-empty, ≤64 KiB, no NUL or ESC), refuses
// stopped / failed / disconnected agents and closed tasks (nothing is
// stored), queues the message, and runs Drain once: a ready agent gets it
// now; otherwise it waits (`queued · agent working`), and an unknown agent
// keeps it with "attach to deliver" until the user cancels it.
//
// The pane is probed first, so an agent whose pane died since the last
// reconcile is recorded as stopped / failed / disconnected and refused.
func (c *Controller) Send(ctx context.Context, req SendRequest) (SendResult, error) {
	if err := domain.ValidateMessageBody(req.Body); err != nil {
		return SendResult{}, userErr(ErrInvalidMessage, err, "%s", err.Error())
	}
	task, err := c.store.Tasks().Get(ctx, req.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		return SendResult{}, userErr(ErrTaskNotFound, err, "task %q not found", req.TaskID)
	}
	if err != nil {
		return SendResult{}, err
	}
	if task.State == domain.TaskClosed {
		return SendResult{}, userErr(ErrSendRejected, nil, "send rejected: %s is closed", task.ID)
	}
	sess, err := c.store.Sessions().LatestForTask(ctx, task.ID)
	if errors.Is(err, store.ErrNotFound) {
		return SendResult{}, userErr(ErrSendRejected, err, "send rejected: %s has no agent session yet", task.ID)
	}
	if err != nil {
		return SendResult{}, err
	}
	if !sess.State.IsTerminal() && !sess.Identity.IsZero() {
		if live, _ := c.liveSession(ctx, sess); live.Status != domain.LivenessUnverified {
			if err := c.reconcileSession(ctx, sess, live); err != nil {
				return SendResult{}, err
			}
			if sess, err = c.store.Sessions().Get(ctx, sess.ID); err != nil {
				return SendResult{}, err
			}
		}
	}
	if domain.SendRejected(sess.State) {
		_, why := domain.CanDeliver(domain.DeliveryCheck{State: sess.State, Confidence: sess.Confidence})
		return SendResult{}, userErr(ErrSendRejected, nil, "send rejected: %s's agent is %s (%s); nothing was queued", task.ID, sess.State, why)
	}
	source := req.Source
	if source == "" {
		source = SendSourceCLI
	}
	m, err := c.store.Messages().Enqueue(ctx, store.NewMessage{TaskID: task.ID, SessionID: sess.ID, Body: req.Body, Source: source})
	if err != nil {
		return SendResult{}, err
	}
	var res SendResult
	dr, derr := c.Drain(ctx, sess.ID)
	if derr != nil {
		res.DrainErr = OneLine(derr.Error())
	}
	res.Delivered = dr.Delivered && dr.MessageID == m.ID
	if res.Message, err = c.Message(ctx, m.ID); err != nil {
		return res, err
	}
	return res, nil
}
