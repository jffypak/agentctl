package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// STUB (m2-tui-inbox) — owned and rewritten whole by m2-tui-manage.
//
// newDiffView opens the diff view for task (key d, from the list or the
// detail): the file list of the task's diff vs its base; enter opens the full
// diff in the pager via tea.ExecProcess (m1-inspect FullDiffCmd) and returns
// to the same view; works for disconnected tasks; records ui.action{diff}.
// All controller work runs in tea.Cmds using sh.ctx; Update never blocks.
// Returning a nil view keeps the current screen; the stub only flashes.
func newDiffView(sh *shared, task controller.TaskView) (view, tea.Cmd) {
	return nil, comingIn("diff", "m2-tui-manage")
}
