package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// margin is the blank column kept on each side of the screen.
const margin = 1

// render composes the whole screen: header, optional banner and filter line,
// the body (list, detail, or the top view), optional flash, footer. Every line
// is clipped to the width and the body is padded so the footer sits on the
// last row.
func (m Model) render() string {
	w, h := max(m.width, 1), max(m.height, 1)
	inner := max(w-2*margin, 1)
	top := m.headerLines(inner)
	bottom := m.footerLines(inner)
	bodyH := max(h-len(top)-len(bottom), 0)
	f := frame{width: inner, height: bodyH, st: m.st}
	body := splitLines(m.body(f))
	if len(body) > bodyH {
		body = body[:bodyH]
	}
	for len(body) < bodyH {
		body = append(body, "")
	}
	lines := append(append(top, body...), bottom...)
	if len(lines) > h { // tiny terminals: keep the header, drop the rest
		lines = lines[:h]
	}
	clip := lipgloss.NewStyle().MaxWidth(inner)
	pad := strings.Repeat(" ", margin)
	for i, l := range lines {
		if l == "" {
			continue
		}
		lines[i] = pad + clip.Render(l)
	}
	return strings.Join(lines, "\n")
}

func (m Model) body(f frame) string {
	switch {
	case len(m.views) > 0:
		return m.views[len(m.views)-1].Body(f)
	case m.sh.ctl == nil:
		return ""
	case m.mode == modeDetail:
		return m.detail.Body(f)
	case !m.loaded:
		return m.listBody(f)
	}
	return m.withQuota(f, m.listBody)
}

// headerLines: `agents / <repo> · N running · M need you` (exactly
// Overview.Header()), a rule, then at most one banner line and the filter line.
func (m Model) headerLines(width int) []string {
	lines := []string{m.headerText(), m.st.rule.Render(strings.Repeat("─", width))}
	if b := m.banner(); b != "" {
		lines = append(lines, m.st.errText.Render(truncate("! "+b, width)))
	}
	if m.filter.active() && len(m.views) == 0 && m.mode == modeList {
		lines = append(lines, m.filterLine(width))
	}
	return lines
}

func (m Model) headerText() string {
	if !m.loaded {
		return m.st.scope.Render("agents")
	}
	scope := m.ov.Repo
	if m.ov.AllRepos || scope == "" {
		scope = "all repos"
	}
	need := m.st.counts
	if m.ov.Counts.NeedsYou > 0 {
		need = m.st.countsNeed
	}
	return m.st.scope.Render("agents / "+scope) +
		m.st.counts.Render(" · "+strconv.Itoa(m.ov.Counts.Running)+" running · ") +
		need.Render(strconv.Itoa(m.ov.Counts.NeedsYou)+" need you")
}

// banner is the one environment problem worth a persistent line, most severe
// first: registry unopenable, registry unreadable, missing tools, reconcile
// failures, startup warnings.
func (m Model) banner() string {
	for _, b := range []string{m.openErr, m.loadErr, m.preflightErr, m.reconcileErr, m.warning} {
		if b != "" {
			return b
		}
	}
	return ""
}

func (m Model) filterLine(width int) string {
	label := m.st.muted.Render("/ ")
	if m.filter.editing {
		return label + m.filter.input.View(m.st, max(width-2, 1), true)
	}
	return label + m.st.text.Render(m.filter.query()) + m.st.muted.Render("  (esc clears)")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
