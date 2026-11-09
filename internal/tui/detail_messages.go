package tui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// messagesSection is the detail view's prompt-queue section: a summary of
// the task's messages by state (from the Overview, so it shows at once),
// then the newest messages with their live delivery state, loaded by
// controller.Messages in a tea.Cmd and reloaded whenever the counts or the
// agent's state change. ↑/↓ select a message; sh.keys.CancelMessage cancels
// the selected one if it is still queued.
type messagesSection struct {
	sh       *shared
	task     controller.TaskView
	msgs     []controller.MessageView
	loaded   bool
	selID    int64 // selected message id (0: the newest)
	seq      int
	inFlight bool
	sentAt   time.Time
	stale    bool
}

// messagesLoadedMsg is one controller.Messages answer.
type messagesLoadedMsg struct {
	taskID string
	seq    int
	msgs   []controller.MessageView
	err    error
}

// messageCancelledMsg is one controller.CancelMessage answer.
type messageCancelledMsg struct {
	taskID string
	v      controller.MessageView
	err    error
}

const (
	// messageRowsSmall / messageRowsLarge bound the listed messages so the
	// diff and output rows below stay visible.
	messageRowsSmall, messageRowsLarge = 3, 6
	// largeHeight is the body height from which the larger bound applies.
	largeHeight = 30
	// messagesRetry: a request older than this is sent again on a refresh
	// (an answer can be lost if it arrives while another view is on top).
	messagesRetry = 2 * time.Second
)

func newMessagesSection(sh *shared, task controller.TaskView) (messagesSection, tea.Cmd) {
	return messagesSection{sh: sh, task: task}.load()
}

func (s messagesSection) load() (messagesSection, tea.Cmd) {
	if s.sh == nil || s.sh.ctl == nil {
		return s, nil
	}
	s.seq++
	s.inFlight, s.stale, s.sentAt = true, false, time.Now()
	sh, id, seq := s.sh, s.task.ID, s.seq
	return s, func() tea.Msg {
		msgs, err := sh.ctl.Messages(sh.ctx, id)
		return messagesLoadedMsg{taskID: id, seq: seq, msgs: msgs, err: err}
	}
}

func (s messagesSection) Update(msg tea.Msg) (messagesSection, tea.Cmd) {
	switch msg := msg.(type) {
	case messagesLoadedMsg:
		if msg.taskID != s.task.ID || msg.seq != s.seq {
			return s, nil
		}
		s.inFlight = false
		if msg.err == nil {
			s.msgs, s.loaded = msg.msgs, true
		}
		if s.stale {
			return s.load()
		}
	case refreshedMsg:
		v, ok := findTask(msg.ov, s.task.ID)
		if !ok {
			return s, nil
		}
		changed := messagesKeyOf(v) != messagesKeyOf(s.task)
		s.task = v
		switch {
		case s.inFlight && time.Since(s.sentAt) > messagesRetry:
			return s.load()
		case changed && s.inFlight:
			s.stale = true
		case changed:
			return s.load()
		}
	case messageCancelledMsg:
		if msg.taskID != s.task.ID {
			return s, nil
		}
		if msg.err != nil {
			return s, flashErr(msg.err)
		}
		s, cmd := s.load()
		return s, tea.Batch(cmd, flashInfo("#"+strconv.FormatInt(msg.v.ID, 10)+" cancelled"), func() tea.Msg { return reloadMsg{} })
	case tea.KeyPressMsg:
		return s.key(msg)
	}
	return s, nil
}

func (s messagesSection) key(k tea.KeyPressMsg) (messagesSection, tea.Cmd) {
	keys := s.sh.keys
	i := s.selected()
	switch {
	case len(s.msgs) == 0:
		return s, nil
	case key.Matches(k, keys.Up):
		s.selID = s.msgs[max(i-1, 0)].ID
	case key.Matches(k, keys.Down):
		s.selID = s.msgs[min(i+1, len(s.msgs)-1)].ID
	case key.Matches(k, keys.CancelMessage):
		m := s.msgs[i]
		if m.State != domain.MsgQueued {
			return s, flashInfo("#" + strconv.FormatInt(m.ID, 10) + " is " + string(m.State) + "; only queued messages can be cancelled")
		}
		sh, taskID, id := s.sh, s.task.ID, m.ID
		return s, func() tea.Msg {
			v, err := sh.ctl.CancelMessage(sh.ctx, id)
			return messageCancelledMsg{taskID: taskID, v: v, err: err}
		}
	}
	return s, nil
}

// selected is the index of the selected message (the newest by default).
func (s messagesSection) selected() int {
	for i, m := range s.msgs {
		if m.ID == s.selID {
			return i
		}
	}
	return len(s.msgs) - 1
}

// messagesKey is what makes a reload worthwhile: counts or agent state moved.
type messagesKey struct {
	counts domain.MessageSummary
	state  domain.AgentState
	conf   domain.Confidence
	since  int64
}

func messagesKeyOf(v controller.TaskView) messagesKey {
	return messagesKey{counts: v.Messages, state: v.AgentState, conf: v.Confidence, since: v.StateSince.UnixMilli()}
}

// View renders `messages  1 queued · 2 acknowledged`, then one row per
// listed message: `> #12 queued · agent working · run the tests …`.
func (s messagesSection) View(f frame) string {
	summary := messageSummary(s.task.Messages)
	if summary == "" && len(s.msgs) == 0 {
		return ""
	}
	if summary == "" {
		summary = "—"
	}
	lines := []string{fieldLine(f, "messages", summary)}
	if !s.loaded || len(s.msgs) == 0 {
		return lines[0]
	}
	limit := messageRowsSmall
	if f.height >= largeHeight {
		limit = messageRowsLarge
	}
	sel := s.selected()
	start := max(len(s.msgs)-limit, 0)
	if sel < start {
		start = sel
	}
	end := min(start+limit, len(s.msgs))
	for i := start; i < end; i++ {
		lines = append(lines, s.row(f, s.msgs[i], i == sel && len(s.msgs) > 1))
	}
	return strings.Join(lines, "\n")
}

func (s messagesSection) row(f frame, m controller.MessageView, selected bool) string {
	st := f.st
	marker := "  "
	if selected {
		marker = st.marker.Render(">") + " "
	}
	style := st.text
	switch m.State {
	case domain.MsgUnconfirmed, domain.MsgFailed:
		style = st.labelNeed
	case domain.MsgQueued:
		if strings.HasSuffix(m.Label, domain.LabelAttach) || strings.HasSuffix(m.Label, domain.LabelStale) {
			style = st.labelNeed
		}
	case domain.MsgAcknowledged, domain.MsgCancelled:
		style = st.muted
	}
	head := "#" + strconv.FormatInt(m.ID, 10) + " " + safeLine(m.Label)
	w := max(f.width-fieldLabelWidth-2, 1)
	text := style.Render(truncate(head, w))
	if rest := w - lipgloss.Width(truncate(head, w)) - 3; rest > 0 && m.Preview != "" {
		text += st.muted.Render(" · " + truncate(safeLine(m.Preview), rest))
	}
	return strings.Repeat(" ", fieldLabelWidth) + marker + text
}

// messageSummary counts messages by state, most urgent first.
func messageSummary(m domain.MessageSummary) string {
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
	return strings.Join(parts, " · ")
}

// Hints adds the cancel key when a queued message is listed.
func (s messagesSection) Hints() []key.Binding {
	for _, m := range s.msgs {
		if m.State == domain.MsgQueued {
			return []key.Binding{help(s.sh.keys.CancelMessage, "cancel queued")}
		}
	}
	return nil
}
