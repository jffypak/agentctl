// Package pager resolves and launches the user's pager for long read-only
// views (full diffs, full pane captures).
//
// Resolution: $AGENTCTL_PAGER, then $PAGER, then `less -R`. Like git, the
// pager line is run through `sh -c`, so a user pager may carry arguments and
// shell syntax (`delta --paging=always`, `less -FRX`); PATH comes from the
// environment the caller passes, not this process's. Callers write plain
// output instead of paging when stdout is not a terminal (IsTerminal).
package pager

import (
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// EnvPager overrides $PAGER for agentctl only.
const EnvPager = "AGENTCTL_PAGER"

// DefaultPager is used when neither variable is set (-R passes the color
// escapes of `git diff --color=always` through).
const DefaultPager = "less -R"

// shell runs the pager line.
const shell = "/bin/sh"

// Resolve returns the pager command line from an environment lookup. Blank
// values fall through to the next source.
func Resolve(getenv func(string) string) string {
	for _, k := range []string{EnvPager, "PAGER"} {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
	}
	return DefaultPager
}

// Command builds `sh -c <pager>` with environ as its environment. Stdin,
// Stdout and Stderr are left unset: the caller sets Stdin to the content and,
// in a CLI, Stdout/Stderr to the terminal; tea.ExecProcess fills any unset
// stream with the terminal itself.
func Command(getenv func(string) string, environ []string) *exec.Cmd {
	cmd := exec.Command(shell, "-c", Resolve(getenv))
	cmd.Env = append([]string(nil), environ...)
	return cmd
}

// IsTerminal reports whether w is a terminal (an *os.File on a tty).
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
