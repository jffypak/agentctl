package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// autoAdoptMsg is one controller.AutoAdopt pass. manual is true for
// sh.keys.AdoptAll: then "nothing to adopt" and every skip are reported too.
// The pass at startup (Options.AutoAdopt) reports only what it adopted.
type autoAdoptMsg struct {
	res    controller.AutoAdoptResult
	err    error
	manual bool
}

// autoAdoptCmd runs controller.AutoAdopt in a tea.Cmd (invariant 1: all
// state changes go through the controller).
func autoAdoptCmd(sh *shared, manual bool) tea.Cmd {
	return func() tea.Msg {
		res, err := sh.ctl.AutoAdopt(sh.ctx)
		return autoAdoptMsg{res: res, err: err, manual: manual}
	}
}

// applyAutoAdopt flashes the result, records ui.action{adopt} per adopted
// task and reloads the list.
func (m Model) applyAutoAdopt(msg autoAdoptMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if msg.manual {
			m.flash = flashMsg{text: "adopt all: " + controller.OneLine(msg.err.Error()), isErr: true}
		}
		return m, nil
	}
	var cmds []tea.Cmd
	names := make([]string, 0, len(msg.res.Adopted))
	for _, a := range msg.res.Adopted {
		names = append(names, a.TaskID)
		cmds = append(cmds, m.sh.actionCmd(controller.UIActionAdopt, a.TaskID))
	}
	text := ""
	switch {
	case len(names) > 0:
		text = "adopted " + plural(len(names), "agent session") + ": " + strings.Join(names, ", ")
	case msg.manual:
		text = "no unregistered agent panes to adopt"
	}
	if msg.manual && len(msg.res.Skipped) > 0 {
		text = strings.TrimPrefix(text+" · skipped "+msg.res.Skipped[0], " · ")
		if n := len(msg.res.Skipped) - 1; n > 0 {
			text += " (+" + strconv.Itoa(n) + " more)"
		}
	}
	if text != "" {
		m.flash = flashMsg{text: safeLine(text)}
	}
	if len(names) > 0 {
		cmds = append(cmds, func() tea.Msg { return reloadMsg{} })
	}
	return m, tea.Batch(cmds...)
}
