package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// lineInput is a minimal single-line text editor (filter now; reusable by the
// new-task form). bubbles/v2 textinput and textarea pull in
// github.com/atotto/clipboard, which is not in go.sum, and no card may add a
// module (docs/notes/m2-tui-inbox.md). The cursor is a reverse-video cell
// that never blinks.
type lineInput struct {
	value []rune
	pos   int // cursor index into value
	limit int // max runes (0 = unlimited)
}

// Value is the current text.
func (in lineInput) Value() string { return string(in.value) }

// SetValue replaces the text and puts the cursor at the end.
func (in lineInput) SetValue(s string) lineInput {
	in.value = []rune(cleanInput(s))
	if in.limit > 0 && len(in.value) > in.limit {
		in.value = in.value[:in.limit]
	}
	in.pos = len(in.value)
	return in
}

// Update applies an editing key or a paste. handled is false for keys the
// editor does not use (enter, esc, tab, ...), which the caller then handles.
func (in lineInput) Update(msg tea.Msg) (out lineInput, handled bool) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		return in.insert(cleanInput(msg.Content)), true
	case tea.KeyPressMsg:
		return in.key(msg)
	}
	return in, false
}

func (in lineInput) key(k tea.KeyPressMsg) (lineInput, bool) {
	switch k.String() {
	case "backspace", "ctrl+h":
		if in.pos > 0 {
			in.value = append(in.value[:in.pos-1:in.pos-1], in.value[in.pos:]...)
			in.pos--
		}
	case "delete", "ctrl+d":
		if in.pos < len(in.value) {
			in.value = append(in.value[:in.pos:in.pos], in.value[in.pos+1:]...)
		}
	case "left", "ctrl+b":
		in.pos = max(in.pos-1, 0)
	case "right", "ctrl+f":
		in.pos = min(in.pos+1, len(in.value))
	case "home", "ctrl+a":
		in.pos = 0
	case "end", "ctrl+e":
		in.pos = len(in.value)
	case "ctrl+u":
		in.value, in.pos = append([]rune(nil), in.value[in.pos:]...), 0
	case "ctrl+k":
		in.value = append([]rune(nil), in.value[:in.pos]...)
	case "ctrl+w":
		start := in.pos
		for start > 0 && in.value[start-1] == ' ' {
			start--
		}
		for start > 0 && in.value[start-1] != ' ' {
			start--
		}
		in.value, in.pos = append(append([]rune(nil), in.value[:start]...), in.value[in.pos:]...), start
	default:
		if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt|tea.ModSuper|tea.ModHyper|tea.ModMeta) != 0 {
			return in, false
		}
		return in.insert(cleanInput(k.Text)), true
	}
	return in, true
}

func (in lineInput) insert(s string) lineInput {
	r := []rune(s)
	if in.limit > 0 {
		r = r[:min(len(r), max(in.limit-len(in.value), 0))]
	}
	v := make([]rune, 0, len(in.value)+len(r))
	v = append(append(append(v, in.value[:in.pos]...), r...), in.value[in.pos:]...)
	in.value, in.pos = v, in.pos+len(r)
	return in
}

// View renders the text in at most width cells with the cursor cell in
// reverse video (when focused), scrolling so the cursor stays visible.
func (in lineInput) View(st styles, width int, focused bool) string {
	if width <= 0 {
		return ""
	}
	start := 0
	if in.pos >= width {
		start = in.pos - width + 1
	}
	vis := in.value[start:min(len(in.value), start+width)]
	cur := in.pos - start
	if !focused {
		return string(vis)
	}
	var b strings.Builder
	b.WriteString(string(vis[:min(cur, len(vis))]))
	if cur < len(vis) {
		b.WriteString(st.cursor.Render(string(vis[cur])))
		b.WriteString(string(vis[cur+1:]))
	} else {
		b.WriteString(st.cursor.Render(" "))
	}
	return b.String()
}

// cleanInput turns newlines and tabs into spaces and drops other control
// characters, so a paste can never smuggle escape sequences into the screen.
func cleanInput(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}
