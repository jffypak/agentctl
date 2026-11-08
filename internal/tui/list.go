package tui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// List geometry.
const (
	maxNameWidth = 24
	minNameWidth = 6
	maxRepoWidth = 16
	ageWidth     = 4 // "59s", "12m", "47h", "3d"
	gap          = 2
)

// emptyText is the ARCHITECTURE.md §User flows empty state (key names are
// bold instead of the doc's backticks).
const emptyText = "No agents yet — n new · a adopt a running claude pane"

// listBody renders the attention sections, or an empty / no-match / loading
// state. Sections keep a blank line between them when everything fits.
func (m Model) listBody(f frame) string {
	if !m.loaded {
		return m.st.muted.Render("loading…")
	}
	rows := m.rows()
	if len(rows) == 0 {
		return m.emptyBody(f)
	}
	sel := max(indexOf(rows, m.selected), 0)
	lines, selLine := m.sectionLines(f, rows, sel, true)
	if len(lines) > f.height {
		lines, selLine = m.sectionLines(f, rows, sel, false)
	}
	if f.height > 0 && selLine >= f.height { // keep the selected row on screen
		lines = lines[selLine-f.height+1:]
	}
	return strings.Join(lines, "\n")
}

func (m Model) emptyBody(f frame) string {
	if m.filter.query() != "" {
		return m.st.text.Render("No tasks match “"+m.filter.query()+"”") + "\n" + m.st.muted.Render("esc clears the filter")
	}
	k := m.st.hintKey
	lines := []string{
		"No agents yet — " + k.Render("n") + " new · " + k.Render("a") + " adopt a running claude pane",
	}
	if m.sh.binding != "" {
		lines = append(lines, "", m.st.muted.Render("Open this from anywhere in tmux; add to tmux.conf:"))
		for _, l := range wrapWords(m.sh.binding, f.width) {
			lines = append(lines, m.st.text.Render(l))
		}
	}
	if n := m.ov.Counts.Closed; n > 0 {
		lines = append(lines, "", m.st.muted.Render(strconv.Itoa(n)+" closed · /closed lists them"))
	}
	return strings.Join(lines, "\n")
}

// sectionLines renders rows grouped by section and returns the line index of
// the selected row.
func (m Model) sectionLines(f frame, rows []controller.TaskView, sel int, spaced bool) ([]string, int) {
	cols := m.columns(f.width, rows)
	var (
		lines   []string
		selLine int
		cur     domain.Section
	)
	for i, r := range rows {
		if i == 0 || r.Attention.Section != cur {
			cur = r.Attention.Section
			if i > 0 && spaced {
				lines = append(lines, "")
			}
			lines = append(lines, m.sectionTitle(cur))
		}
		if i == sel {
			selLine = len(lines)
		}
		lines = append(lines, m.row(r, cols, i == sel))
	}
	return lines, selLine
}

func (m Model) sectionTitle(s domain.Section) string {
	switch s {
	case domain.SectionNeedsYou:
		return m.st.sectionNeed.Render(s.Title())
	case domain.SectionUnknown, domain.SectionClosed:
		return m.st.unknown.Render(s.Title())
	}
	return m.st.section.Render(s.Title())
}

// columns sizes the name / repo / text columns for this width.
type columns struct {
	name, repo, text int
	showRepo         bool
}

func (m Model) columns(width int, rows []controller.TaskView) columns {
	c := columns{showRepo: m.ov.AllRepos}
	for _, r := range rows {
		c.name = max(c.name, lipgloss.Width(r.Name))
		c.repo = max(c.repo, lipgloss.Width(r.Repo))
	}
	c.name = min(max(c.name, minNameWidth), maxNameWidth)
	c.repo = min(c.repo, maxRepoWidth)
	fixed := 2 + c.name + gap + gap + ageWidth // marker, name, gaps, age
	if c.showRepo {
		fixed += c.repo + gap
	}
	c.text = max(width-fixed, 0)
	return c
}

// row renders one task: marker, name, [repo], state text, age.
func (m Model) row(r controller.TaskView, c columns, selected bool) string {
	st := m.st
	marker, name := "  ", st.name
	if selected {
		marker, name = st.marker.Render(">")+" ", st.nameSelected
	}
	label, detail := rowText(r)
	labelStyle, detailStyle := st.text, st.muted
	switch r.Attention.Section {
	case domain.SectionNeedsYou:
		labelStyle = st.labelNeed
	case domain.SectionUnknown, domain.SectionClosed:
		labelStyle, detailStyle, name = st.unknown, st.unknown, st.unknown.Bold(selected)
	}
	text := truncate(label, c.text)
	styled, used := labelStyle.Render(text), lipgloss.Width(text)
	if rest := c.text - used; detail != "" && rest > 3 && text == label {
		d := truncate(" · "+detail, rest)
		styled += detailStyle.Render(d)
		used += lipgloss.Width(d)
	}
	pad := c.text - used
	var b strings.Builder
	b.WriteString(marker)
	b.WriteString(name.Render(padRight(truncate(r.Name, c.name), c.name)))
	b.WriteString(strings.Repeat(" ", gap))
	if c.showRepo {
		b.WriteString(st.muted.Render(padRight(truncate(r.Repo, c.repo), c.repo)))
		b.WriteString(strings.Repeat(" ", gap))
	}
	b.WriteString(styled)
	b.WriteString(strings.Repeat(" ", max(pad, 0)+gap))
	b.WriteString(st.muted.Render(padLeft(formatAge(r.Age), ageWidth)))
	return b.String()
}

// rowText is the state label and the last detail, never repeating the label
// (a disconnected row's detail is "disconnected: pane_missing").
func rowText(r controller.TaskView) (label, detail string) {
	label = r.Attention.Label
	detail = oneLine(domain.SanitizeTerminalText(r.LastDetail))
	if strings.HasPrefix(detail, label) {
		detail = strings.TrimLeft(strings.TrimPrefix(detail, label), ": ")
	}
	if r.Attention.Section == domain.SectionClosed {
		detail = ""
	}
	return label, detail
}

// formatAge renders a compact age: 12s, 4m, 3h, 2d.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
}

// truncate cuts plain text to width cells, ending in "…" when cut.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > width-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	return b.String() + "…"
}

// truncateLeft keeps the end of s (paths), starting with "…" when cut.
func truncateLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	used, i := 0, len(runes)
	for i > 0 {
		rw := lipgloss.Width(string(runes[i-1]))
		if used+rw > width-1 {
			break
		}
		used += rw
		i--
	}
	return "…" + string(runes[i:])
}

func padRight(s string, width int) string {
	return s + strings.Repeat(" ", max(width-lipgloss.Width(s), 0))
}

func padLeft(s string, width int) string {
	return strings.Repeat(" ", max(width-lipgloss.Width(s), 0)) + s
}

// oneLine collapses whitespace so stored multi-line text fits a row.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// wrapWords wraps plain text at spaces into lines of at most width cells
// (a word longer than width gets its own line).
func wrapWords(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = w
		case lipgloss.Width(cur)+1+lipgloss.Width(w) <= width:
			cur += " " + w
		default:
			lines = append(lines, cur)
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
