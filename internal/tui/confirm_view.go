package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// STUB (m2-tui-inbox) — owned and rewritten whole by m2-tui-manage.
//
// newCloseConfirmView asks "close <task>? y/N" (key x; default No — any key
// but sh.keys.Confirm cancels). "close and stop" is offered only for
// launched sessions and needs a second explicit confirmation (spec §4.5: no
// key silently performs a destructive action). On success it returns a nil
// view plus tea.Batch(sh.actionCmd(controller.UIActionClose, task.ID),
// func() tea.Msg { return reloadMsg{} }).
//
// Returning a nil view keeps the current screen; the stub only flashes.
func newCloseConfirmView(sh *shared, task controller.TaskView) (view, tea.Cmd) {
	return nil, comingIn("close", "m2-tui-manage")
}
