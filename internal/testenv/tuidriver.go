package testenv

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/tmux"
)

// TUIWait bounds every TUI wait-for. It is generous on purpose: the waits
// are on screen content, so a healthy run returns as soon as the frame is
// drawn, and only a broken one pays the bound (load-sensitivity lesson: an
// earlier harness that bounded commands at 200ms dropped them under -race).
const TUIWait = 30 * time.Second

// TUIOptions configures StartTUI.
type TUIOptions struct {
	Session string   // tmux session name the TUI runs in (default "ui")
	Cwd     string   // absolute start directory (the repo the TUI scopes to)
	Width   int      // pane width (default 120)
	Height  int      // pane height (default 40)
	Args    []string // agentctl arguments (default none: bare agentctl = TUI)
}

// TUI is the real agentctl binary running on a real terminal: a pane of the
// isolated tmux server. Tests drive it as a user would, with send-keys, and
// read it with capture-pane wait-fors. The pane keeps remain-on-exit, so the
// exit status and the final screen stay readable after the TUI exits.
type TUI struct {
	srv     *TmuxServer
	Session string
	Target  string // "=<session>:" (exact-match session target)
}

// StartTUI runs bin (agentctl) in a new detached session of s. The pane's
// environment is exactly env (minus TMUX/TMUX_PANE, which tmux sets itself
// for the pane, so the TUI knows it runs inside this server) plus
// AGENTCTL_TMUX_SOCKET pointing at s, each passed with -e.
func (s *TmuxServer) StartTUI(t testing.TB, bin string, env Env, opts TUIOptions) *TUI {
	t.Helper()
	if opts.Session == "" {
		opts.Session = "ui"
	}
	if opts.Width <= 0 {
		opts.Width = 120
	}
	if opts.Height <= 0 {
		opts.Height = 40
	}
	if opts.Cwd == "" {
		t.Fatal("StartTUI: Cwd is required (the TUI scopes to the cwd's repo)")
	}
	newSession := []string{"new-session", "-d", "-s", opts.Session, "-c", opts.Cwd,
		"-x", strconv.Itoa(opts.Width), "-y", strconv.Itoa(opts.Height)}
	for _, kv := range env.Without("TMUX", "TMUX_PANE").With(tmux.EnvSocket + "=" + s.Socket).Strings() {
		newSession = append(newSession, "-e", kv)
	}
	newSession = append(newSession, "--", bin)
	newSession = append(newSession, opts.Args...)
	if len(opts.Args) == 0 {
		// tmux runs a single argv element through the shell; a second
		// element makes it exec bin directly. "tui" is bare agentctl.
		newSession = append(newSession, "tui")
	}
	ctx, cancel := context.WithTimeout(context.Background(), tmuxCallTimeout)
	defer cancel()
	// One invocation: an agentctl that exits at once (a refused start) must
	// still leave its pane, status and screen behind.
	if _, err := s.Runner().Run(ctx, tmux.Request{
		Socket:   tmux.Socket{Path: s.Socket},
		Commands: [][]string{newSession, {"set-option", "-w", "remain-on-exit", "on"}},
	}); err != nil {
		t.Fatalf("start the TUI in session %s: %v", opts.Session, err)
	}
	return &TUI{srv: s, Session: opts.Session, Target: "=" + opts.Session + ":"}
}

// Screen is the visible screen (capture-pane -p), "" when it cannot be read.
func (u *TUI) Screen() string {
	out, _ := u.srv.TryRun("capture-pane", "-p", "-t", u.Target)
	return out
}

// WaitScreen waits (TUIWait) until cond holds for the visible screen and
// returns that screen. On timeout it fails with the last screen.
func (u *TUI) WaitScreen(t testing.TB, what string, cond func(screen string) bool) string {
	t.Helper()
	var last string
	WaitFor(t, TUIWait, what, func() (bool, string) {
		last = u.Screen()
		return cond(last), "\n" + last
	})
	return last
}

// WaitText waits until the screen contains every one of texts.
func (u *TUI) WaitText(t testing.TB, texts ...string) string {
	t.Helper()
	return u.WaitScreen(t, strings.Join(texts, " + "), func(s string) bool {
		for _, x := range texts {
			if !strings.Contains(s, x) {
				return false
			}
		}
		return true
	})
}

// Keys sends tmux key names (j, Enter, Escape, C-s, ...) to the TUI.
// Escape must be sent alone: an ESC that arrives in the same read as the
// next key is an alt+key to the terminal parser, not escape.
func (u *TUI) Keys(t testing.TB, keys ...string) {
	t.Helper()
	if len(keys) > 1 && slices.Contains(keys, "Escape") {
		t.Fatalf("Keys(%q): send Escape alone and wait for the redraw before the next key", keys)
	}
	u.srv.Run(t, append([]string{"send-keys", "-t", u.Target}, keys...)...)
}

// Type sends text literally (send-keys -l): every character is a key press.
func (u *TUI) Type(t testing.TB, text string) {
	t.Helper()
	u.srv.Run(t, "send-keys", "-t", u.Target, "-l", text)
}

// SelectedRow is the list row marked with the selection cursor ("> ").
func SelectedRow(screen string) string {
	for _, l := range strings.Split(screen, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " "), "> ") {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// listRows returns the task names of the list rows, top to bottom, and the
// index of the selected one (-1 if none). A row is " > name ..." (selected)
// or "   name ..."; section titles are indented by one space only.
//
// A row may carry the unread column ("● " or two spaces before the name), and
// a wide screen has the details pane right of " │", which is ignored.
func listRows(screen string) (names []string, selected int) {
	selected = -1
	for _, l := range strings.Split(ListColumn(screen), "\n") {
		name, isSel, ok := RowName(l)
		if !ok {
			continue
		}
		if isSel {
			selected = len(names)
		}
		names = append(names, name)
	}
	return names, selected
}

// ListColumn is screen with the details pane (from " │") cut off every
// line, so row and section checks see the list alone. The cut is at " │",
// not " │ ": capture-pane trims the space after the bar when the pane line
// is empty.
func ListColumn(screen string) string {
	lines := strings.Split(screen, "\n")
	for i, l := range lines {
		if left, _, ok := strings.Cut(l, " │"); ok {
			lines[i] = strings.TrimRight(left, " ")
		}
	}
	return strings.Join(lines, "\n")
}

// RowName parses one list line: the task name, whether the row is selected
// (" > "), and ok when the line is a task row at all. The unread dot column
// is skipped.
func RowName(l string) (name string, selected, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(l, " > "):
		rest, selected = l[3:], true
	case strings.HasPrefix(l, "   "):
		rest = l[3:]
	default:
		return "", false, false
	}
	f := strings.Fields(strings.TrimPrefix(strings.TrimLeft(rest, " "), "●"))
	if len(f) == 0 {
		return "", false, false
	}
	return f[0], selected, true
}

// Select moves the list cursor with j/k, one key at a time and waiting for
// each redraw, until the selected row is the task name.
func (u *TUI) Select(t testing.TB, name string) {
	t.Helper()
	for range 50 {
		var names []string
		var sel, want int
		u.WaitScreen(t, "a selected row and "+name, func(s string) bool {
			names, sel = listRows(s)
			want = slices.Index(names, name)
			return sel >= 0 && want >= 0
		})
		if sel == want {
			return
		}
		key := "j"
		if want < sel {
			key = "k"
		}
		from := names[sel]
		u.Keys(t, key)
		u.WaitScreen(t, "the cursor to leave "+from, func(s string) bool {
			n, i := listRows(s)
			return i >= 0 && n[i] != from
		})
	}
	t.Fatalf("cannot select %q:\n%s", name, u.Screen())
}

// PanePID is the pid of the TUI process.
func (u *TUI) PanePID(t testing.TB) int {
	t.Helper()
	pid, err := strconv.Atoi(u.srv.Run(t, "display-message", "-p", "-t", u.Target, "#{pane_pid}"))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// Alive reports whether the TUI process is still running.
func (u *TUI) Alive(t testing.TB) bool {
	t.Helper()
	return u.srv.Run(t, "display-message", "-p", "-t", u.Target, "#{pane_dead}") == "0"
}

// Hangup sends SIGHUP to the TUI, as tmux does when a popup is closed.
func (u *TUI) Hangup(t testing.TB) {
	t.Helper()
	if err := syscall.Kill(u.PanePID(t), syscall.SIGHUP); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
}

// WaitExit waits for the TUI process to end and returns its exit status.
func (u *TUI) WaitExit(t testing.TB) string {
	t.Helper()
	WaitFor(t, TUIWait, "the TUI to exit", func() (bool, string) {
		out, _ := u.srv.TryRun("display-message", "-p", "-t", u.Target, "#{pane_dead} #{pane_dead_status}")
		return strings.HasPrefix(out, "1 "), out + "\n" + u.Screen()
	})
	return u.srv.Run(t, "display-message", "-p", "-t", u.Target, "#{pane_dead_status}")
}

// History is the pane's whole scrollback plus screen, wrapped lines joined.
func (u *TUI) History() string {
	out, _ := u.srv.TryRun("capture-pane", "-p", "-J", "-S", "-", "-t", u.Target)
	return out
}
