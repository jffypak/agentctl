package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// STUB (m2-tui-inbox) — owned and rewritten whole by m2-tui-manage.
//
// newLogsView opens the logs view for task (key l, from the list or the
// detail): scrollable recent capture; sh.keys.FullCapture writes the full
// capture and pages it; a mismatched pane shows 'pane unavailable'; records
// ui.action{logs}.
// All controller work runs in tea.Cmds using sh.ctx; Update never blocks.
// Returning a nil view keeps the current screen; the stub only flashes.
func newLogsView(sh *shared, task controller.TaskView) (view, tea.Cmd) {
	return nil, comingIn("logs", "m2-tui-manage")
}
