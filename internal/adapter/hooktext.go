package adapter

import (
	"strings"
	"unicode/utf8"

	"github.com/jffypak/agentctl/internal/domain"
)

// HookLine sanitizes terminal control sequences in hook-supplied text and
// folds whitespace runs (including newlines and tabs) into single spaces.
func HookLine(s string) string {
	return strings.Join(strings.Fields(domain.SanitizeTerminalText(s)), " ")
}

// MaxHookText bounds each multi-line hook text kept for the task details
// (last prompt, last message).
const MaxHookText = 4096

// HookText sanitizes multi-line hook text for display (newlines and tabs
// stay, every terminal control goes), trims it, and caps it at MaxHookText
// bytes with a trailing "…".
func HookText(s string) string {
	s = strings.TrimSpace(domain.SanitizeTerminalText(s))
	if len(s) > MaxHookText {
		s = strings.TrimSpace(Bound(s, MaxHookText-len("…"))) + "…"
	}
	return s
}

// Bound truncates s to at most n bytes on a rune boundary.
func Bound(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
