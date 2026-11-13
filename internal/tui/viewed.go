package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// viewedCmd records that the user looked at task (detail, logs, prompt,
// attach), which clears its unread mark, then reloads the list so the mark
// goes away at once (MarkViewed writes no event the poll would see).
func viewedCmd(sh *shared, task controller.TaskView) tea.Cmd {
	if sh.ctl == nil || task.ID == "" {
		return nil
	}
	return func() tea.Msg {
		if err := sh.ctl.MarkViewed(sh.ctx, task.ID); err != nil {
			return flashMsg{text: "could not mark " + task.ID + " read: " + controller.OneLine(err.Error()), isErr: true}
		}
		if !task.Unread {
			return nil
		}
		return reloadMsg{}
	}
}

// dockedViewed keeps what is on screen read: a turn that ends while its
// agent is docked beside the list, or while its detail is open, is seen as
// it happens.
func (m Model) dockedViewed() tea.Cmd {
	var cmds []tea.Cmd
	for _, id := range []string{m.docked, m.detailID()} {
		if t, ok := findTask(m.ov, id); ok && t.Unread {
			cmds = append(cmds, viewedCmd(m.sh, t))
		}
	}
	return tea.Batch(cmds...)
}

func (m Model) detailID() string {
	if m.mode == modeDetail {
		return m.detail.task.ID
	}
	return ""
}
