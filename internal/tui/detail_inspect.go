package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// STUB (m2-tui-inbox) — owned and rewritten whole by m2-tui-manage.
//
// inspectSection is the detail view's diff summary + last ~10 output lines,
// loaded asynchronously: the constructor returns the loading command (using
// sh.ctx and the m1-inspect controller API), View shows "loading…" until the
// result message arrives, and Update must never block (>50ms). The detail
// view forwards every message it does not handle (refreshedMsg included) and
// renders View below the fields and the messages section.
//
// The stub shows nothing.
type inspectSection struct {
	sh   *shared
	task controller.TaskView
}

func newInspectSection(sh *shared, task controller.TaskView) (inspectSection, tea.Cmd) {
	return inspectSection{sh: sh, task: task}, nil
}

func (s inspectSection) Update(msg tea.Msg) (inspectSection, tea.Cmd) { return s, nil }

func (s inspectSection) View(f frame) string { return "" }
