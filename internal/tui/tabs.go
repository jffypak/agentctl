package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// tabID is one of the list tabs. Each tab narrows the listed rows on top of
// the `/` filter; tab / shift+tab cycle through them.
type tabID int

const (
	tabAll tabID = iota
	tabNeedsYou
	tabUnread
	tabRunning
	tabIdle
	tabCount
)

func (t tabID) label() string {
	switch t {
	case tabNeedsYou:
		return "Needs you"
	case tabUnread:
		return "Unread"
	case tabRunning:
		return "Running"
	case tabIdle:
		return "Idle"
	}
	return "All"
}

// matches reports whether row v belongs on tab t.
func (t tabID) matches(v controller.TaskView) bool {
	switch t {
	case tabNeedsYou:
		return v.Attention.Section == domain.SectionNeedsYou
	case tabUnread:
		return v.Unread
	case tabRunning:
		return v.Attention.Section == domain.SectionRunning
	case tabIdle:
		return v.Attention.Section == domain.SectionIdle
	}
	return true
}

// next cycles forward (delta 1) or back (delta -1).
func (t tabID) next(delta int) tabID {
	return tabID((int(t) + delta + int(tabCount)) % int(tabCount))
}

// tabsHint is the right-aligned key hint of the tab line.
const tabsHint = "tab/⇧tab"

// tabsLine renders `All 11  Needs you 6  Unread 2  Running 1  Idle 2`, the
// active tab in reverse video. Counts follow the `/` filter, so they always
// match what each tab would list.
func (m Model) tabsLine(width int) string {
	filtered := visibleRows(m.ov.Rows, m.filter.query())
	st := m.st
	var b strings.Builder
	used := 0
	for t := range tabCount {
		n := 0
		for _, r := range filtered {
			if t.matches(r) {
				n++
			}
		}
		text := " " + t.label() + " " + strconv.Itoa(n) + " "
		style := st.muted
		switch {
		case t == m.tab:
			style = st.cursor.Bold(true)
		case n > 0 && t == tabNeedsYou:
			style = st.countsNeed
		case n > 0 && t == tabUnread:
			style = st.text.Bold(true)
		}
		if used > 0 {
			b.WriteString(" ")
			used++
		}
		b.WriteString(style.Render(text))
		used += lipgloss.Width(text)
	}
	if rest := width - used; rest > lipgloss.Width(tabsHint)+2 {
		b.WriteString(strings.Repeat(" ", rest-lipgloss.Width(tabsHint)))
		b.WriteString(st.muted.Render(tabsHint))
	}
	return b.String()
}
