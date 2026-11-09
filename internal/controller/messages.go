package controller

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// Message errors (errors.Is; the *Error carries the one-line text).
var (
	ErrMessageNotFound = errors.New("message not found")
	ErrCancelRefused   = errors.New("message cannot be cancelled")
)

// MessageView is one prompt as the presentation layers see it. The body is
// never shown in full: Preview is its first line, sanitized and bounded.
type MessageView struct {
	ID        int64
	TaskID    string
	SessionID int64
	Preview   string
	Lines     int
	Bytes     int
	State     domain.MessageState
	AckMatch  domain.AckMatch
	Label     string // e.g. "queued · agent working", "acknowledged (hash)"
	CreatedAt time.Time
	SentAt    time.Time
	AckedAt   time.Time
	Error     string
}

// PreviewRunes bounds MessageView.Preview.
const PreviewRunes = 60

// messageListLimit bounds Messages.
const messageListLimit = 20

// Messages returns the task's newest messages (oldest first) with live labels
// computed from its session's stored state.
func (c *Controller) Messages(ctx context.Context, taskID string) ([]MessageView, error) {
	list, err := c.store.Messages().ForTask(ctx, taskID, messageListLimit)
	if err != nil {
		return nil, err
	}
	sessions := map[int64]store.Session{}
	out := make([]MessageView, 0, len(list))
	for _, m := range list {
		sess, ok := sessions[m.SessionID]
		if !ok {
			if sess, err = c.store.Sessions().Get(ctx, m.SessionID); err != nil {
				return nil, err
			}
			sessions[m.SessionID] = sess
		}
		out = append(out, c.messageView(m, sess))
	}
	return out, nil
}

func (c *Controller) messageView(m store.Message, sess store.Session) MessageView {
	v := MessageView{
		ID: m.ID, TaskID: m.TaskID, SessionID: m.SessionID, Preview: preview(m.Body),
		Lines: strings.Count(strings.TrimRight(m.Body, "\n"), "\n") + 1, Bytes: len(m.Body),
		State: m.State, AckMatch: m.AckMatch, CreatedAt: m.CreatedAt, SentAt: m.SentAt, AckedAt: m.AckedAt,
		Error: OneLine(domain.SanitizeTerminalText(m.Error)),
	}
	v.Label = messageLabel(m, sess, c.staleHook(sess))
	return v
}

// messageLabel is the delivery state a person reads.
func messageLabel(m store.Message, sess store.Session, stale bool) string {
	switch m.State {
	case domain.MsgQueued:
		if why := domain.QueueLabel(sess.State, sess.Confidence, stale); why != "" {
			return "queued · " + why
		}
		return "queued · next turn"
	case domain.MsgDelivering:
		return "delivering"
	case domain.MsgSent:
		return "sent · awaiting ack"
	case domain.MsgAcknowledged:
		if m.AckMatch != "" {
			return "acknowledged (" + string(m.AckMatch) + ")"
		}
		return "acknowledged"
	case domain.MsgUnconfirmed:
		return "unconfirmed · attach to check"
	case domain.MsgFailed:
		return "failed"
	case domain.MsgCancelled:
		return "cancelled"
	}
	return string(m.State)
}

// preview is the body's first non-empty line, sanitized, at most
// PreviewRunes runes ("…" when cut or when more lines follow).
func preview(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r", ""), "\n")
	first, more := "", false
	for i, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			first = OneLine(domain.SanitizeTerminalText(l))
			more = strings.TrimSpace(strings.Join(lines[i+1:], "")) != ""
			break
		}
	}
	if utf8.RuneCountInString(first) > PreviewRunes {
		return string([]rune(first)[:PreviewRunes-1]) + "…"
	}
	if more {
		return first + " …"
	}
	return first
}

// Message returns one message by id.
func (c *Controller) Message(ctx context.Context, id int64) (MessageView, error) {
	m, err := c.store.Messages().Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return MessageView{}, userErr(ErrMessageNotFound, err, "message %d not found", id)
	}
	if err != nil {
		return MessageView{}, err
	}
	sess, err := c.store.Sessions().Get(ctx, m.SessionID)
	if err != nil {
		return MessageView{}, err
	}
	return c.messageView(m, sess), nil
}

// CancelMessage cancels a queued message. A message that is being
// delivered, was already pasted, or is finished cannot be taken back.
func (c *Controller) CancelMessage(ctx context.Context, id int64) (MessageView, error) {
	m, err := c.store.Messages().Cancel(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return MessageView{}, userErr(ErrMessageNotFound, err, "message %d not found", id)
	case errors.Is(err, store.ErrMessageState):
		return MessageView{}, userErr(ErrCancelRefused, err, "message %d is %s; only queued messages can be cancelled", id, m.State)
	case err != nil:
		return MessageView{}, err
	}
	return c.Message(ctx, m.ID)
}
