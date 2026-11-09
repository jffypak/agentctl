package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// STUB (m2-tui-inbox) — owned and rewritten whole by m2-prompt-queue.
//
// messagesSection is the detail view's prompt-queue section: each message's
// delivery state, updated live, with sh.keys.CancelMessage cancelling the
// selected queued one. The detail view constructs it when it opens, forwards
// every message it does not handle itself (keys other than its own and
// refreshedMsg included), renders View under the detail fields, and adds
// Hints to its footer. Rows should use fieldLine for the label column.
//
// The stub renders the message counts the Overview already carries.
type messagesSection struct {
	sh   *shared
	task controller.TaskView
}

func newMessagesSection(sh *shared, task controller.TaskView) (messagesSection, tea.Cmd) {
	return messagesSection{sh: sh, task: task}, nil
}

func (s messagesSection) Update(msg tea.Msg) (messagesSection, tea.Cmd) {
	if m, ok := msg.(refreshedMsg); ok {
		if v, found := findTask(m.ov, s.task.ID); found {
			s.task = v
		}
	}
	return s, nil
}

func (s messagesSection) View(f frame) string {
	m := s.task.Messages
	var parts []string
	for _, c := range []struct {
		n     int
		label string
	}{{m.Unconfirmed, "unconfirmed"}, {m.Failed, "failed"}, {m.Queued, "queued"}, {m.Delivering, "delivering"},
		{m.Sent, "sent"}, {m.Acknowledged, "acknowledged"}, {m.Cancelled, "cancelled"}} {
		if c.n > 0 {
			parts = append(parts, strconv.Itoa(c.n)+" "+c.label)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return fieldLine(f, "messages", strings.Join(parts, " · "))
}

func (s messagesSection) Hints() []key.Binding { return nil }
