package tui

import tea "charm.land/bubbletea/v2"

// STUB (m2-tui-inbox) — owned and rewritten whole by m2-tui-manage.
//
// newNewTaskView opens the name + goal form (key n): inline slug validation,
// controller.NewTask run asynchronously with Cwd sh.cwd, branch-exists /
// not-a-repo / claude-missing errors shown inline with the form kept. On
// success it returns a nil view plus
// tea.Batch(sh.actionCmd(controller.UIActionNew, id), func() tea.Msg { return selectTaskMsg{id: id} })
// so the new row shows selected as starting. lineInput (input.go) is the
// text field (bubbles textinput needs a module that is not in go.sum).
//
// Returning a nil view keeps the current screen; the stub only flashes.
func newNewTaskView(sh *shared) (view, tea.Cmd) {
	return nil, comingIn("new task", "m2-tui-manage")
}
