package tui

import (
	"slices"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func TestLineInputEditing(t *testing.T) {
	type step struct {
		msg     tea.Msg
		value   string
		pos     int
		handled bool
	}
	k := keyMsg
	steps := []step{
		{k("a"), "a", 1, true},
		{k("b"), "ab", 2, true},
		{k("space"), "ab ", 3, true},
		{k("界"), "ab 界", 4, true},
		{tea.KeyPressMsg{Code: tea.KeyLeft}, "ab 界", 3, true},
		{k("x"), "ab x界", 4, true},
		{k("backspace"), "ab 界", 3, true},
		{tea.KeyPressMsg{Code: tea.KeyHome}, "ab 界", 0, true},
		{tea.KeyPressMsg{Code: tea.KeyDelete}, "b 界", 0, true},
		{tea.KeyPressMsg{Code: tea.KeyEnd}, "b 界", 3, true},
		{tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}, "b ", 2, true},
		{tea.PasteMsg{Content: "one\ntwo\x1b[31m\x00"}, "b one two[31m", 13, true},
		{tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}, "b one two[31m", 0, true},
		{tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}, "", 0, true},
		{k("enter"), "", 0, false},
		{k("esc"), "", 0, false},
		{tea.KeyPressMsg{Code: 'x', Mod: tea.ModAlt, Text: "x"}, "", 0, false},
	}
	var in lineInput
	for i, s := range steps {
		var handled bool
		in, handled = in.Update(s.msg)
		if in.Value() != s.value || in.pos != s.pos || handled != s.handled {
			t.Fatalf("step %d (%v): value %q pos %d handled %v, want %q %d %v", i, s.msg, in.Value(), in.pos, handled, s.value, s.pos, s.handled)
		}
	}
	in = in.SetValue("hello world")
	in, _ = in.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if in.Value() != "" {
		t.Fatalf("ctrl+u at end = %q", in.Value())
	}
	limited := lineInput{limit: 3}.SetValue("abcdef")
	if limited.Value() != "abc" {
		t.Fatalf("limit = %q", limited.Value())
	}
	if limited, _ = limited.Update(k("z")); limited.Value() != "abc" {
		t.Fatalf("typing past the limit = %q", limited.Value())
	}
	st := newStyles(true, true)
	if v := plain(lineInput{}.SetValue("abcdefgh").View(st, 4, true)); v != "fgh " {
		t.Fatalf("scrolled view = %q", v)
	}
	if v := (lineInput{}).View(st, 0, true); v != "" {
		t.Fatalf("zero-width view = %q", v)
	}
}

func TestKeyMapCoversEveryActionAndCopiesOnWrite(t *testing.T) {
	km := DefaultKeyMap()
	seen := map[string]string{}
	for _, a := range Actions() {
		b, ok := km.Binding(a)
		if !ok || len(b.Keys()) == 0 || b.Help().Desc == "" {
			t.Fatalf("action %s has no default binding", a)
		}
		for _, k := range b.Keys() {
			if other, dup := seen[k]; dup {
				t.Errorf("key %q bound to %s and %s", k, other, a)
			}
			seen[k] = a
		}
	}
	if _, ok := km.Binding("dance"); ok {
		t.Fatal("unknown action resolved")
	}
	km2, err := km.WithKeys(ActPrompt, "P", "ctrl+p")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(km.Prompt.Keys(), []string{"p"}) || !slices.Equal(km2.Prompt.Keys(), []string{"P", "ctrl+p"}) ||
		km2.Prompt.Help().Key != "P/ctrl+p" || km2.Prompt.Help().Desc != "prompt" {
		t.Fatalf("WithKeys: original %v copy %v %+v", km.Prompt.Keys(), km2.Prompt.Keys(), km2.Prompt.Help())
	}
	if !key.Matches(keyMsg("P"), km2.Prompt) || key.Matches(keyMsg("p"), km2.Prompt) {
		t.Fatal("rebinding does not match")
	}
	if _, err := km.WithKeys("dance", "z"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if _, err := km.WithKeys(ActQuit); err == nil {
		t.Fatal("empty key list accepted")
	}
	for k, want := range map[string]*key.Binding{"enter": &km.Open, "esc": &km.Back, "up": &km.Up, "down": &km.Down,
		"ctrl+c": &km.ForceQuit, "/": &km.Filter, "j": &km.Down, "k": &km.Up} {
		if !key.Matches(keyMsg(k), *want) {
			t.Errorf("%s does not match its default binding", k)
		}
	}
}

func TestCustomKeyMapDrivesTheShell(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	km, err := DefaultKeyMap().WithKeys(ActDown, "s")
	if err != nil {
		t.Fatal(err)
	}
	m := press(t, f.model(t, 120, 40, Options{Keys: &km, Warning: "config.yaml: unknown action \"dance\"; using defaults"}), "s")
	if s, _ := m.selectedTask(); s.ID != "flaky-ci" {
		t.Fatalf("custom down key: selected %s", s.ID)
	}
	if !strings.Contains(screen(m)[2], `! config.yaml: unknown action "dance"; using defaults`) {
		t.Fatalf("startup warning banner missing:\n%s", screenText(m))
	}
}
