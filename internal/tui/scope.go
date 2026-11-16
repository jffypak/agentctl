package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// toggleScope is keys.Scope: the list switches between every repo (the
// default) and the repo containing the TUI's working directory, then
// reloads. Outside a repo both scopes list every repo.
func (m Model) toggleScope() (tea.Model, tea.Cmd) {
	if m.sh.ctl == nil {
		return m, nil
	}
	this := !m.sh.thisRepo.Load()
	m.sh.thisRepo.Store(this)
	m.flash = flashMsg{text: "listing every repo"}
	if this {
		m.flash = flashMsg{text: "listing this repo only (" + m.sh.keys.Scope.Help().Key + " lists every repo)"}
	}
	return m, func() tea.Msg { return reloadMsg{} }
}

// scopeHint names what keys.Scope switches to.
func (m Model) scopeHint() key.Binding {
	if m.sh.thisRepo.Load() {
		return help(m.sh.keys.Scope, "all repos")
	}
	return help(m.sh.keys.Scope, "this repo")
}
