package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// STUB (m2-tui-inbox) — owned and rewritten whole by m2-prompt-queue.
//
// newComposeView opens the prompt composer for task (key p, from the list or
// the detail). Contract for the real view (docs/notes/m2-tui-inbox.md):
//   - it is a view (view.go): Update gets every key while it is on top, so
//     typed q / n / j are text; ctrl+s (sh.keys.Send) sends, esc
//     (sh.keys.Back) cancels and returns a nil view;
//   - sending calls the controller asynchronously (a tea.Cmd using sh.ctx)
//     and never blocks Update; on success it returns
//     tea.Batch(sh.actionCmd(controller.UIActionPrompt, task.ID), func() tea.Msg { return reloadMsg{} })
//     and a nil view; a rejected send (stopped/failed/disconnected) shows
//     the one-line error inline and keeps the text;
//   - it receives refreshedMsg while open and may show the live label
//     (e.g. "queued · agent working").
//
// Returning a nil view keeps the current screen; the stub only flashes.
func newComposeView(sh *shared, task controller.TaskView) (view, tea.Cmd) {
	return nil, comingIn("prompt", "m2-prompt-queue")
}
