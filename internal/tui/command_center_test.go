package tui

import (
	"slices"
	"strings"
	"testing"
)

func listedIDs(m Model) []string {
	var out []string
	for _, r := range m.rows() {
		out = append(out, r.ID)
	}
	return out
}

// TestTabsNarrowTheList: tab / shift+tab cycle All -> Needs you -> Unread ->
// Running -> Idle -> All, each tab lists only its rows, and the selection
// falls back to the tab's first row.
func TestTabsNarrowTheList(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedCommandCenter(t)
	m := f.model(t, 120, 40, Options{})
	if len(listedIDs(m)) != 12 || m.tab != tabAll {
		t.Fatalf("All lists %v", listedIDs(m))
	}
	want := map[tabID][]string{
		tabNeedsYou: {"legacy-pane", "flaky-ci", "migrate-db", "auth-tests", "api-docs", "silent-agent"},
		tabUnread:   {"api-docs", "docs-site"},
		tabRunning:  {"payments"},
		tabIdle:     {"old-spike", "docs-site"},
	}
	for _, tab := range []tabID{tabNeedsYou, tabUnread, tabRunning, tabIdle} {
		m = press(t, m, "tab")
		if m.tab != tab || !slices.Equal(listedIDs(m), want[tab]) {
			t.Fatalf("tab %s lists %v, want %v", tab.label(), listedIDs(m), want[tab])
		}
		if sel, _ := m.selectedTask(); sel.ID != want[tab][0] {
			t.Fatalf("tab %s selects %s", tab.label(), sel.ID)
		}
	}
	if m = press(t, m, "tab"); m.tab != tabAll {
		t.Fatalf("tab after Idle = %s", m.tab.label())
	}
	if m = press(t, m, "shift+tab"); m.tab != tabIdle {
		t.Fatalf("shift+tab from All = %s", m.tab.label())
	}
	// the `/` filter narrows the counts too
	m = press(t, m, "tab", "/", "d", "o", "c", "s", "enter")
	if line := screen(m)[3]; !strings.Contains(line, "All 2") || !strings.Contains(line, "Unread 2") {
		t.Fatalf("filtered tab line = %q", line)
	}
}

// TestOpeningATaskClearsUnread: ↵ (detail) marks the task viewed, the list
// reloads, and the dot and the Unread count go away. Opening the logs or the
// composer does the same.
func TestOpeningATaskClearsUnread(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedCommandCenter(t)
	m := f.model(t, 120, 40, Options{})
	m = press(t, m, "tab", "tab") // Unread: api-docs, docs-site
	m = run(t, press(t, m, "j"), nil)
	m, cmd := pressCmd(t, m, "enter") // docs-site detail
	m = run(t, m, cmd)
	m = press(t, m, "esc")
	if got := listedIDs(m); !slices.Equal(got, []string{"api-docs"}) {
		t.Fatalf("Unread lists %v after opening docs-site", got)
	}
	if line := screen(m)[2]; !strings.Contains(line, "Unread 1") {
		t.Fatalf("tab line = %q", line)
	}
	m, cmd = pressCmd(t, m, "l") // logs on api-docs
	m = run(t, m, cmd)
	if m.ov.Counts.Unread != 0 {
		t.Fatalf("unread after logs = %d", m.ov.Counts.Unread)
	}
}

// TestDetailsPaneOnlyWhenWide: the pane shows the selected task beside the
// list from 120 columns; an 80-column popup keeps the list alone.
func TestDetailsPaneOnlyWhenWide(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedCommandCenter(t)
	wide := press(t, f.model(t, 120, 40, Options{}), "tab", "tab") // api-docs selected
	pane := strings.Join(paneOf(wide), "\n")
	for _, want := range []string{"api-docs", "● unread", "claude · claude-opus-5", "Last message", "Regenerated the reference", "Prompt"} {
		if !strings.Contains(pane, want) {
			t.Fatalf("pane has no %q:\n%s", want, pane)
		}
	}
	narrow := press(t, f.model(t, 80, 24, Options{}), "tab", "tab")
	if p := paneOf(narrow); len(p) != 0 || strings.Contains(screenText(narrow), paneSep) {
		t.Fatalf("80 columns show a pane:\n%s", screenText(narrow))
	}
}

func TestWrapText(t *testing.T) {
	got := wrapText("one two three\n\n\n\nfour\x1b[31m five\nabcdefghijkl", 6)
	want := []string{"one", "two", "three", "", "four", "five", "abcdef", "ghijkl"}
	if !slices.Equal(got, want) {
		t.Fatalf("wrapText = %q, want %q", got, want)
	}
	if got := clipRows([]string{"a", "b", "c"}, 2, 10); !slices.Equal(got, []string{"a", "b …"}) {
		t.Fatalf("clipRows = %q", got)
	}
	if got := stripMarkdown("## Title\n**bold** and `code`"); got != "Title\nbold and code" {
		t.Fatalf("stripMarkdown = %q", got)
	}
}
