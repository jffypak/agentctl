package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// Details pane geometry: the list shows the selected task's details on its
// right once the screen has room for both (paneMinScreen columns inside the
// margins); narrower screens keep ↵ for the full detail.
const (
	paneMinScreen = 118
	paneMinWidth  = 36
	paneMaxWidth  = 64
	paneSep       = " │ "
	maxPromptRows = 4 // the prompt is context; the last message gets the room
)

// paneWidth is the details pane's width for an inner screen width, 0 when
// the screen is too narrow for it.
func paneWidth(inner int) int {
	if inner < paneMinScreen {
		return 0
	}
	return min(max(inner*2/5, paneMinWidth), paneMaxWidth)
}

// withDetailsPane renders the list body on the left and the selected task's
// details on the right, when the screen is wide enough and a task is
// selected; otherwise the list alone.
func (m Model) withDetailsPane(f frame, list func(frame) string) string {
	pw := paneWidth(f.width)
	task, ok := m.selectedTask()
	if pw == 0 || !ok {
		return list(f)
	}
	left := f
	left.width = f.width - pw - lipgloss.Width(paneSep)
	right := f
	right.width = pw
	ll, rl := splitLines(list(left)), m.detailsLines(right, task)
	sep := m.st.rule.Render(paneSep)
	out := make([]string, f.height)
	for i := range out {
		var l, r string
		if i < len(ll) {
			l = ll[i]
		}
		if i < len(rl) {
			r = rl[i]
		}
		out[i] = padRight(l, left.width) + sep + r
	}
	return strings.Join(out, "\n")
}

// detailsLines is the pane: name, state, agent and model, branch, worktree,
// the agent's last message (most of the room) and the last prompt.
func (m Model) detailsLines(f frame, t controller.TaskView) []string {
	st, w := f.st, f.width
	label := m.st.text
	switch t.Attention.Section {
	case domain.SectionNeedsYou:
		label = st.labelNeed
	case domain.SectionInitial, domain.SectionUnknown, domain.SectionClosed:
		label = st.unknown
	}
	state := t.Attention.Section.Title() + " · " + t.Attention.Label
	if t.Unread {
		state = unreadDot + " unread · " + state
	}
	agent := "—"
	if t.HasSession {
		agent = safeLine(t.AgentType)
		if t.Model != "" {
			agent += " · " + safeLine(t.Model)
		}
	}
	lines := []string{
		st.nameSelected.Render(truncate(t.Name, w)),
		label.Render(truncate(state, w)),
		"",
		st.muted.Render("Agent  ") + truncate(agent, w-7),
		st.muted.Render("Branch ") + truncate(safeLine(t.Branch), w-7),
		st.muted.Render(truncateLeft(safeLine(controller.DisplayPath(t.Worktree)), w)),
		"",
	}

	prompt, promptLabel := t.LastPrompt, "Prompt"
	if prompt == "" && t.Goal != "" {
		prompt, promptLabel = t.Goal, "Goal"
	}
	var promptRows []string
	if prompt != "" {
		promptRows = clipRows(wrapText(prompt, w), maxPromptRows, w)
	}
	// rows left for the message: the frame minus the fixed lines, the
	// message label, and the prompt block (blank, label, rows)
	room := f.height - len(lines) - 1
	if len(promptRows) > 0 {
		room -= 2 + len(promptRows)
	}

	lines = append(lines, st.muted.Render("Last message"))
	switch msg := t.LastMessage; {
	case msg == "":
		lines = append(lines, st.muted.Render("none yet"))
	case room > 0:
		for _, l := range clipRows(wrapText(stripMarkdown(msg), w), room, w) {
			lines = append(lines, st.text.Render(l))
		}
	}
	if len(promptRows) > 0 {
		lines = append(lines, "", st.muted.Render(promptLabel))
		for _, l := range promptRows {
			lines = append(lines, st.text.Render(l))
		}
	}
	if len(lines) > f.height {
		lines = lines[:f.height]
	}
	return lines
}

// clipRows keeps the first n rows (each at most width cells), ending the
// last kept one in "…" when rows were dropped.
func clipRows(rows []string, n, width int) []string {
	if n <= 0 {
		return nil
	}
	if len(rows) <= n {
		return rows
	}
	out := append([]string(nil), rows[:n]...)
	last := out[n-1]
	if lipgloss.Width(last)+2 > width {
		last = truncate(last, width-1) // ends in "…" already
		out[n-1] = strings.TrimSuffix(last, "…") + "…"
		return out
	}
	out[n-1] = last + " …"
	return out
}

// wrapText wraps stored multi-line text to width: paragraphs keep their
// line breaks, runs of blank lines fold into one, and words longer than the
// width are cut. The text was sanitized at ingest; it is sanitized again
// here (not with safeText, which quotes multi-line text like a path).
func wrapText(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var out []string
	blank := false
	for _, para := range strings.Split(domain.SanitizeTerminalText(s), "\n") {
		if strings.TrimSpace(para) == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		for _, l := range wrapWords(para, width) {
			for lipgloss.Width(l) > width {
				cut := cutWidth(l, width)
				out = append(out, l[:cut])
				l = l[cut:]
			}
			out = append(out, l)
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// cutWidth is the byte length of the longest prefix of s at most width
// cells wide (at least one rune).
func cutWidth(s string, width int) int {
	used := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > width && i > 0 {
			return i
		}
		used += rw
	}
	return len(s)
}

// stripMarkdown drops the markup characters agents use most (bold markers,
// heading hashes, inline code ticks) so the message reads as plain text in a
// narrow pane.
func stripMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		t := strings.TrimLeft(l, " ")
		if h := strings.TrimLeft(t, "#"); h != t && strings.HasPrefix(h, " ") {
			l = strings.TrimSpace(h)
		}
		l = strings.ReplaceAll(l, "**", "")
		l = strings.ReplaceAll(l, "`", "")
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}
