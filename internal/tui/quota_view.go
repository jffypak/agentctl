package tui

import (
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// quotaRefresh is how often the quota panel re-reads its sources. Quota
// moves slowly and Codex's source is a file scan, so this is far slower than
// the event poll.
const quotaRefresh = 15 * time.Second

// quotaLow is the share left (percent) below which a window is highlighted.
const quotaLow = 20

// quotaStale is how old a snapshot gets before the panel names its age (a
// Claude snapshot only updates while some Claude session is working).
const quotaStale = 2 * time.Minute

type (
	quotaTickMsg struct{}
	// quotaMsg is one quota read.
	quotaMsg struct {
		snaps []controller.QuotaSnapshot
		err   error
	}
)

func quotaCmd(sh *shared) tea.Cmd {
	return func() tea.Msg {
		snaps, err := sh.ctl.Quota(sh.ctx)
		return quotaMsg{snaps: snaps, err: err}
	}
}

// applyQuota keeps the newest snapshots (an unreadable source keeps the
// other one) and schedules the next read.
func (m Model) applyQuota(q quotaMsg) (tea.Model, tea.Cmd) {
	if q.err == nil || len(q.snaps) > 0 {
		m.quota = q.snaps
	}
	return m, tick(quotaRefresh, quotaTickMsg{})
}

// Quota panel geometry.
const (
	quotaAgentWidth  = 8  // "claude", "codex"
	quotaWindowWidth = 16 // "100%  in 23h59m"
)

// quotaLines renders the panel shown under the list: a title row, then one
// row per agent with the share left in each window and when it resets.
// Nothing when no agent has reported quota yet.
func (m Model) quotaLines(now time.Time) []string {
	if len(m.quota) == 0 {
		return nil
	}
	st := m.st
	title := st.section.Render(padRight("QUOTA", quotaAgentWidth)) +
		st.muted.Render(padRight("5h left", quotaWindowWidth)+"  "+"week left")
	lines := []string{title} // render() clips every line to the width
	for _, s := range m.quota {
		var b strings.Builder
		b.WriteString(st.text.Render(padRight(s.Agent, quotaAgentWidth)))
		b.WriteString(m.quotaWindow(s.FiveHour, now, true))
		b.WriteString("  ")
		b.WriteString(m.quotaWindow(s.Weekly, now, false))
		if age := now.Sub(s.CapturedAt); !s.CapturedAt.IsZero() && age >= quotaStale {
			b.WriteString(st.muted.Render("  as of " + formatAge(age) + " ago"))
		}
		lines = append(lines, b.String())
	}
	return lines
}

// quotaWindow renders one window as "58%  in 2h10m", padded to
// quotaWindowWidth when pad is set (every column but the last).
func (m Model) quotaWindow(w *controller.QuotaWindow, now time.Time, pad bool) string {
	width := 0
	if pad {
		width = quotaWindowWidth
	}
	if w == nil {
		return m.st.muted.Render(padRight("—", width))
	}
	left := w.LeftPercent(now)
	pct := padRight(strconv.Itoa(int(left))+"%", 5)
	style := m.st.text
	if left < quotaLow {
		style = m.st.labelNeed
	}
	when := ""
	switch {
	case w.Reset(now):
		when = "reset"
	case !w.ResetsAt.IsZero():
		when = "in " + formatUntil(w.ResetsAt.Sub(now))
	}
	if when == "" {
		return style.Render(padRight(strings.TrimSpace(pct), width))
	}
	return style.Render(pct) + m.st.muted.Render(padRight(when, max(width-5, 0)))
}

// formatUntil renders a time to reset compactly: 45m, 2h10m, 3d4h.
func formatUntil(d time.Duration) string {
	d = max(d.Round(time.Minute), time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d/time.Hour)%24, int(d/time.Minute)%60
	switch {
	case days > 0:
		return strconv.Itoa(days) + "d" + strconv.Itoa(hours) + "h"
	case hours > 0:
		return strconv.Itoa(hours) + "h" + pad2(mins) + "m"
	}
	return strconv.Itoa(mins) + "m"
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// withQuota puts the quota panel at the bottom of a list body of f.height
// lines, with a blank line above it. The list comes first: the panel shows
// only when every list row still fits (compact spacing allowed) in the
// lines left over.
func (m Model) withQuota(f frame, body func(frame) string) string {
	panel := m.quotaLines(m.ov.Now)
	room := f.height - len(panel) - 1
	if len(panel) == 0 || room < 1 || m.listNeed(f) > room {
		return body(f)
	}
	sub := f
	sub.height = room
	lines := splitLines(body(sub))
	if len(lines) > room {
		return body(f)
	}
	for len(lines) < room+1 {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, panel...), "\n")
}

// listNeed is the fewest lines the list needs to show every row (sections
// without blank lines between them); 0 for the empty states, which
// withQuota measures from their output.
func (m Model) listNeed(f frame) int {
	rows := m.rows()
	if len(rows) == 0 {
		return 0
	}
	lines, _ := m.sectionLines(f, rows, 0, false)
	return len(lines)
}
