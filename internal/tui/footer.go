package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// footerLines: an optional flash line, a rule, and the key hints that fit.
func (m Model) footerLines(width int) []string {
	var lines []string
	if m.flash.text != "" {
		if m.flash.isErr {
			lines = append(lines, m.st.errText.Render(truncate("! "+m.flash.text, width)))
		} else {
			lines = append(lines, m.st.muted.Render(truncate(m.flash.text, width)))
		}
	}
	return append(lines, m.st.rule.Render(strings.Repeat("─", width)), m.hintLine(width))
}

// hints are the live bindings for the current context, most useful first;
// the footer drops whatever does not fit.
func (m Model) hints() []key.Binding {
	k := m.sh.keys
	switch {
	case m.sh.ctl == nil:
		return []key.Binding{k.Quit}
	case len(m.views) > 0:
		return m.views[len(m.views)-1].Hints()
	case m.filter.editing:
		return []key.Binding{help(k.Open, "apply"), help(k.Back, "clear"), arrows("move")}
	case m.mode == modeDetail:
		return append([]key.Binding{help(k.Open, "attach"), k.Prompt, k.Diff, k.Logs, k.Close},
			append(m.detail.messages.Hints(), help(k.Back, "back"))...)
	}
	return []key.Binding{k.Open, k.Prompt, k.Diff, k.Logs, k.New, k.Adopt, k.Close, k.Filter, k.Quit, arrows("move")}
}

// help relabels a binding's description for one context.
func help(b key.Binding, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(b.Help().Key, desc))
}

// arrows is a display-only hint for ↑/↓ while typing (j/k are text then).
func arrows(desc string) key.Binding {
	return key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", desc))
}

func (m Model) hintLine(width int) string {
	var b strings.Builder
	used := 0
	for _, h := range m.hints() {
		hk, hd := h.Help().Key, h.Help().Desc
		if hk == "" || !h.Enabled() {
			continue
		}
		w := lipgloss.Width(hk) + 1 + lipgloss.Width(hd)
		sep := 0
		if used > 0 {
			sep = 2
		}
		if used+sep+w > width {
			break
		}
		if sep > 0 {
			b.WriteString("  ")
		}
		b.WriteString(m.st.hintKey.Render(hk) + " " + m.st.muted.Render(hd))
		used += sep + w
	}
	return b.String()
}
