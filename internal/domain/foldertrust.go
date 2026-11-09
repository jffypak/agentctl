package domain

import (
	"strings"
	"unicode"
)

// FolderTrustDetail is the last_detail of a launched agent whose pane shows
// Claude Code's folder-trust dialog (m3-folder-trust-detect). agentctl never
// answers that dialog: the user attaches and answers it.
const FolderTrustDetail = "folder trust: attach to answer"

// folderTrustPhrases are distinctive phrases of Claude Code's folder-trust
// dialog, which it shows before any hook fires on a path it has not seen
// (every new worktree). Lower-case and single-spaced: the matcher normalizes
// the screen the same way.
//
// Source: the 2026-11-09 real two-agent demo (Claude Code 2.1.319, see
// docs/ARCHITECTURE.md §Open questions) showed "Quick safety check: Is this a
// project you created or one you trust?" with the option "1. Yes, I trust
// this folder"; earlier 1.x releases asked "Do you trust the files in this
// folder?". Each phrase is specific to that dialog; generic words ("trust",
// "folder") alone never match. A false match can only produce a hint (an
// unknown, non-deliverable state), never a deliverable one.
var folderTrustPhrases = []string{
	"is this a project you created or one you trust",
	"yes, i trust this folder",
	"do you trust the files in this folder",
}

// ShowsFolderTrustDialog reports whether captured pane text shows Claude
// Code's folder-trust dialog. The text is sanitized here (escape sequences
// never split or forge a phrase), box-drawing borders are dropped, and
// whitespace (including line breaks from the dialog's own wrapping) is
// collapsed before a case-insensitive phrase match.
func ShowsFolderTrustDialog(screen string) bool {
	norm := normalizeScreen(screen)
	if norm == "" {
		return false
	}
	for _, p := range folderTrustPhrases {
		if strings.Contains(norm, p) {
			return true
		}
	}
	return false
}

// normalizeScreen returns sanitized, lower-case, single-spaced text with
// box-drawing characters (U+2500..U+257F) removed.
func normalizeScreen(screen string) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 0x2500 && r <= 0x257F {
			return ' '
		}
		return unicode.ToLower(r)
	}, SanitizeTerminalText(screen))
	return strings.Join(strings.Fields(clean), " ")
}
