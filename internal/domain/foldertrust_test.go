package domain

import (
	"strings"
	"testing"
)

// claudeTrustScreen is Claude Code 2.x's folder-trust dialog as it renders
// on an 80-column pane (wording seen in the 2026-11-09 real demo, 2.1.319):
// Ink wraps the question itself, so a phrase can span two screen lines.
const claudeTrustScreen = "\x1b[1m Accessing workspace:\x1b[0m\n\n" +
	" /Users/me/src/repo-worktrees/alpha\n\n" +
	" Quick safety check: Is this a project you created or one you\n" +
	" trust? (Like your own code, a well-known open source project, or work\n" +
	" from your team). If not, take a moment to review what's in this folder first.\n\n" +
	" Claude Code'll be able to read, edit, and execute files here.\n\n" +
	" \x1b[36m❯ 1. Yes, I trust this folder\x1b[0m\n" +
	"   2. No, exit\n\n" +
	" Enter to confirm · Esc to cancel\n"

func TestShowsFolderTrustDialog(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"claude 2.x dialog, wrapped and colored", claudeTrustScreen, true},
		{"claude 1.x wording inside a box", "╭──────────────────────────────────────╮\n" +
			"│ Do you trust the files in this       │\n" +
			"│ folder?                              │\n" +
			"│ ❯ 1. Yes, proceed                    │\n" +
			"╰──────────────────────────────────────╯\n", true},
		{"upper case", "IS THIS A PROJECT YOU CREATED OR ONE YOU TRUST?", true},
		{"option line alone", "❯ 1. Yes, I trust this folder\n  2. No, exit", true},
		{"phrase split by an escape sequence", "Is this a project you\x1b[0m created or one you trust?", true},
		{"empty", "", false},
		{"ordinary prompt", "fakeagent 1234\n> ", false},
		{"word trust alone", "I trust you will finish the folder work", false},
		{"login dialog", "Select login method:\n ❯ 1. Claude account with subscription", false},
		{"near miss", "Is this a project you created?", false},
	}
	for _, c := range cases {
		if got := ShowsFolderTrustDialog(c.screen); got != c.want {
			t.Errorf("%s: ShowsFolderTrustDialog = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFolderTrustPhrasesAreNormalized(t *testing.T) {
	// The matcher compares against lower-case, single-spaced text: a phrase
	// that is not in that form could never match.
	for _, p := range folderTrustPhrases {
		if p != strings.ToLower(p) || p != strings.Join(strings.Fields(p), " ") || p == "" {
			t.Errorf("phrase %q is not lower-case and single-spaced", p)
		}
	}
}

func TestFolderTrustDetail(t *testing.T) {
	if FolderTrustDetail != "folder trust: attach to answer" {
		t.Fatalf("FolderTrustDetail = %q", FolderTrustDetail)
	}
}
