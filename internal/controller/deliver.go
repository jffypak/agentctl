package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
)

// DeliverRequest is one hook-triggered background delivery.
type DeliverRequest struct {
	SessionID int64
	AfterPID  int       // the hook process; delivery waits for it to exit (0: do not wait)
	Log       io.Writer // one line per step (logs/deliver.log); nil discards
}

// DeliverReport is what DeliverAfterHook did.
type DeliverReport struct {
	Drain        DrainResult
	Acknowledged bool
	AckMatch     domain.AckMatch
	Unconfirmed  bool
	Err          error // logged; `agentctl deliver` still exits 0
}

// hookExitWait bounds the wait for the hook process (its own budget is
// HookBudget; a parent that never reaps it must not stall delivery forever:
// Drain re-verifies everything anyway).
const hookExitWait = 10 * time.Second

// A drain whose claim found the registry busy (nothing written) is retried
// this many times, busyRetryPause apart: deliver is the only drainer a
// session gets per Stop when no TUI is open.
const (
	busyRetries    = 3
	busyRetryPause = 250 * time.Millisecond
)

// ackPoll is how often the ack wait re-reads the message.
const ackPoll = 50 * time.Millisecond

// DeliverAfterHook is `agentctl deliver --session N --after-pid P`, spawned
// detached by the hook that moved the session to ready with messages queued:
// wait for the hook process to exit (its state write has committed),
// DeliverSettle, Drain, then wait up to AckWindow for the UserPromptSubmit
// ack (the hook's ack followup records it; submits that raced the paste are
// matched here too). No ack in time -> unconfirmed. It never returns an
// error: everything is in the report and the log.
func (c *Controller) DeliverAfterHook(ctx context.Context, req DeliverRequest) DeliverReport {
	logf := func(format string, args ...any) {
		if req.Log != nil {
			fmt.Fprintf(req.Log, "%s deliver session=%d: %s\n", time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
				req.SessionID, OneLine(fmt.Sprintf(format, args...)))
		}
	}
	var rep DeliverReport
	if req.AfterPID > 0 {
		if !waitPIDExit(ctx, req.AfterPID, hookExitWait) {
			logf("hook pid %d still running after %v; draining anyway", req.AfterPID, hookExitWait)
		}
	}
	if err := sleepCtx(ctx, c.tun.DeliverSettle); err != nil {
		rep.Err = err
		logf("cancelled: %v", err)
		return rep
	}
	before, err := c.store.Events().LastID(ctx)
	if err != nil {
		rep.Err = err
		logf("error: %v", err)
		return rep
	}
	rep.Drain, err = c.Drain(ctx, req.SessionID)
	for try := 1; err == nil && rep.Drain.Reason == ReasonRegistryBusy && try <= busyRetries; try++ {
		logf("registry busy; drain retry %d/%d", try, busyRetries)
		if err = sleepCtx(ctx, busyRetryPause); err == nil {
			rep.Drain, err = c.Drain(ctx, req.SessionID)
		}
	}
	if err != nil {
		rep.Err = err
		logf("drain error (message %d): %v", rep.Drain.MessageID, err)
		return rep
	}
	if !rep.Drain.Delivered {
		logf("nothing delivered: %s", rep.Drain.Reason)
		return rep
	}
	id := rep.Drain.MessageID
	logf("message %d sent; waiting up to %v for UserPromptSubmit", id, c.tun.AckWindow)
	deadline := time.Now().Add(c.tun.AckWindow)
	for {
		m, err := c.store.Messages().Get(ctx, id)
		if err != nil {
			rep.Err = err
			logf("error reading message %d: %v", id, err)
			return rep
		}
		if m.State == domain.MsgAcknowledged {
			rep.Acknowledged, rep.AckMatch = true, m.AckMatch
			logf("message %d acknowledged (%s)", id, m.AckMatch)
			return rep
		}
		if m.State != domain.MsgSent {
			logf("message %d is %s", id, m.State)
			return rep
		}
		if d, err := c.ackFromSubmits(ctx, req.SessionID, before); err != nil {
			logf("ack check: %v", err)
		} else if d.MessageID == id {
			continue // re-read: acknowledged
		}
		if !time.Now().Before(deadline) {
			break
		}
		if err := sleepCtx(ctx, min(ackPoll, time.Until(deadline))); err != nil {
			rep.Err = err
			logf("cancelled while waiting for the ack of message %d", id)
			return rep
		}
	}
	sentEv, _ := c.store.Messages().MessageEventID(ctx, domain.EventMessageSent, id)
	ok, err := c.store.Messages().MarkUnconfirmed(ctx, id, "no UserPromptSubmit within "+c.tun.AckWindow.String()+" of the paste", sentEv)
	switch {
	case err != nil:
		rep.Err = err
		logf("marking message %d unconfirmed: %v", id, err)
	case ok:
		rep.Unconfirmed = true
		logf("message %d unconfirmed: no UserPromptSubmit within %v", id, c.tun.AckWindow)
	}
	return rep
}

// waitPIDExit polls until pid no longer exists (ESRCH) or timeout.
func waitPIDExit(ctx context.Context, pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if sleepCtx(ctx, 10*time.Millisecond) != nil {
			return false
		}
	}
}

// sleepCtx sleeps for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
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
