package tui

import tea "charm.land/bubbletea/v2"

// STUB (m2-tui-inbox) — owned and rewritten whole by m2-tui-manage.
//
// newAdoptView opens the adopt picker (key a): candidate panes from the
// m1-pane-identity controller API, the user picks one explicitly and names
// it; on success it returns a nil view plus
// tea.Batch(sh.actionCmd(controller.UIActionAdopt, id), func() tea.Msg { return selectTaskMsg{id: id} })
// so the row lands selected in UNKNOWN.
//
// Returning a nil view keeps the current screen; the stub only flashes.
func newAdoptView(sh *shared) (view, tea.Cmd) {
	return nil, comingIn("adopt", "m2-tui-manage")
}
