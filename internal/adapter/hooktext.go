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
