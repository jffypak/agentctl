package tmux

import (
	"fmt"
	"path/filepath"
	"strings"
)

// popupBindingPrefix is the tmux.conf line that opens agentctl in a popup in
// the current pane's directory (ARCHITECTURE.md §User flows: `prefix a`).
const popupBindingPrefix = `bind-key a display-popup -E -w 80% -h 75% -d '#{pane_current_path}' `

// PopupBinding returns the tmux.conf line printed by `agentctl tmux-binding`
// and shown in the TUI empty state:
//
//	bind-key a display-popup -E -w 80% -h 75% -d '#{pane_current_path}' '<abs agentctl>'
//
// The command goes through two parsers: tmux's config parser (which strips the
// single quotes) and the popup's `sh -c`. A path of plain characters is
// single-quoted as is; a path with spaces or other shell-inert characters is
// double-quoted for the shell inside the tmux single quotes. A path containing
// characters that are special to either parser (quotes, $, `, \, #, !,
// newlines) is refused rather than guessed at.
func PopupBinding(agentctlPath string) (string, error) {
	if agentctlPath == "" || !filepath.IsAbs(agentctlPath) {
		return "", fmt.Errorf("tmux binding: agentctl path %q is not absolute", agentctlPath)
	}
	if strings.ContainsAny(agentctlPath, "'\"$`\\#!\n\r\t") {
		return "", fmt.Errorf("tmux binding: agentctl path %q contains characters tmux.conf and sh cannot both quote; move the binary or write the binding by hand", agentctlPath)
	}
	word := agentctlPath
	if !shellPlain(agentctlPath) {
		word = `"` + agentctlPath + `"`
	}
	return popupBindingPrefix + "'" + word + "'", nil
}

// shellPlain reports whether s needs no quoting for sh.
func shellPlain(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("/._+-@%:,=", r):
		default:
			return false
		}
	}
	return true
}
