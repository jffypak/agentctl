package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
)

const tiBinding = `bind-key a display-popup -E -w 80% -h 75% -d '#{pane_current_path}' '/opt/agentctl/bin/agentctl'`

func TestTmuxBindingLine(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	r := runCmd(t, env, t.TempDir(), "tmux-binding")
	if r.code != 0 || r.stdout != tiBinding+"\n" || r.stderr != "" {
		t.Fatalf("tmux-binding = %+v", r)
	}
	if r := runCmd(t, env, t.TempDir(), "tmux-binding", "x"); r.code != 2 {
		t.Fatalf("tmux-binding x = %+v", r)
	}
}

func TestTUIUsage(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	if r := runCmd(t, env, t.TempDir(), "tui", "extra"); r.code != 2 || !strings.Contains(r.stderr, "usage: agentctl [tui]") {
		t.Fatalf("tui extra = %+v", r)
	}
}

// TestHelperAgentctlTUI is not a test: the real-terminal tests below run the
// test binary inside an isolated tmux pane with this selected, so bare
// `agentctl` runs on a real terminal (a pty, a real size, real keys).
func TestHelperAgentctlTUI(t *testing.T) {
	if os.Getenv("AGENTCTL_TEST_HELPER_TUI") != "1" {
		t.Skip("helper process for the real-terminal TUI tests")
	}
	os.Exit(run(context.Background(), nil, osSystem()))
}

// tiPane is bare `agentctl` running in a pane of an isolated tmux server.
type tiPane struct {
	srv    *testenv.TmuxServer
	target string // "=<session>:"
	exe    string
}

// tiStartTUI starts the TUI in a new session on srv (remain-on-exit, so the
// exit status and the final screen stay readable). env supplies AGENTCTL_HOME
// and the agent command; the pane gets the isolated socket explicitly.
func tiStartTUI(t *testing.T, srv *testenv.TmuxServer, env testenv.Env, session, cwd string, width int) tiPane {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"new-session", "-d", "-s", session, "-x", strconv.Itoa(width), "-y", "30", "-c", cwd,
		"-e", "AGENTCTL_TEST_HELPER_TUI=1", "-e", "AGENTCTL_HOME=" + env.Get("AGENTCTL_HOME"),
		"-e", "AGENTCTL_TMUX_SOCKET=" + srv.Socket, "-e", "TERM=xterm-256color"}
	if cmd := env.Get("AGENTCTL_AGENT_CMD"); cmd != "" {
		args = append(args, "-e", "AGENTCTL_AGENT_CMD="+cmd)
	}
	srv.Run(t, append(args, "--", exe, "-test.run=^TestHelperAgentctlTUI$")...)
	p := tiPane{srv: srv, target: "=" + session + ":", exe: exe}
	srv.Run(t, "set-option", "-w", "-t", p.target, "remain-on-exit", "on")
	return p
}

func (p tiPane) screen() string {
	out, _ := p.srv.TryRun("capture-pane", "-p", "-t", p.target)
	return out
}

func (p tiPane) waitFor(t *testing.T, what string, cond func(screen string) bool) {
	t.Helper()
	testenv.WaitFor(t, 15*time.Second, what, func() (bool, string) {
		s := p.screen()
		return cond(s), s
	})
}

// waitExit waits for the TUI process to end and returns its exit status.
func (p tiPane) waitExit(t *testing.T) string {
	t.Helper()
	testenv.WaitFor(t, 10*time.Second, "the TUI to exit", func() (bool, string) {
		out, _ := p.srv.TryRun("display-message", "-p", "-t", p.target, "#{pane_dead} #{pane_dead_status}")
		return strings.HasPrefix(out, "1 "), out
	})
	return p.srv.Run(t, "display-message", "-p", "-t", p.target, "#{pane_dead_status}")
}

// TestTUIQuitLeavesAgentsAlive: quitting the TUI with q, ctrl+c or SIGHUP
// ends only the TUI process (exit 0); every agent session survives
// (has-session, pane alive), and each run is recorded as opened and closed.
func TestTUIQuitLeavesAgentsAlive(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	for _, name := range []string{"one", "two"} {
		if r := runCmd(t, f.env, f.repo.Root, "new", name, "--goal", "g"); r.code != 0 {
			t.Fatalf("new %s = %+v", name, r)
		}
	}
	for i, quit := range []string{"q", "C-c", "SIGHUP"} {
		p := tiStartTUI(t, f.srv, f.env, "ui-"+strconv.Itoa(i), f.repo.Root, 120)
		p.waitFor(t, "the TUI header", func(s string) bool {
			return strings.Contains(s, "agents / repo · 0 running · 0 need you") && strings.Contains(s, "INITIALSTATE") && strings.Contains(s, "two")
		})
		if quit == "SIGHUP" {
			pid, err := strconv.Atoi(f.srv.Run(t, "display-message", "-p", "-t", p.target, "#{pane_pid}"))
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, syscall.SIGHUP); err != nil {
				t.Fatal(err)
			}
		} else {
			f.srv.Run(t, "send-keys", "-t", p.target, quit)
		}
		if status := p.waitExit(t); status != "0" {
			out, _ := f.srv.TryRun("capture-pane", "-p", "-S", "-200", "-t", p.target)
			t.Errorf("%s: TUI exit status %s, want 0; pane:\n%s", quit, status, out)
		}
		for _, agent := range []string{"agentctl-one", "agentctl-two"} {
			if _, err := f.srv.TryRun("has-session", "-t", "="+agent); err != nil {
				t.Fatalf("after %s: %s is gone: %v", quit, agent, err)
			}
			if dead := f.srv.Run(t, "display-message", "-p", "-t", "="+agent+":", "#{pane_dead}"); dead != "0" {
				t.Fatalf("after %s: %s's agent died", quit, agent)
			}
		}
	}
	opened, closed := tiCountEvents(t, f.env, domain.EventUIOpened), tiCountEvents(t, f.env, domain.EventUIClosed)
	if opened != 3 || closed != 3 {
		t.Fatalf("ui.opened %d ui.closed %d, want 3 each", opened, closed)
	}
}

// TestTUIEmptyStateShowsTheTmuxBinding: on a real terminal the empty state
// shows exactly the line `agentctl tmux-binding` prints for the same binary.
func TestTUIEmptyStateShowsTheTmuxBinding(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	p := tiStartTUI(t, f.srv, f.env, "ui", f.repo.Root, 220)
	var out, errb bytes.Buffer
	sys := &system{stdin: strings.NewReader(""), stdout: &out, stderr: &errb, environ: f.env.Strings(),
		cwd: f.repo.Root, now: time.Now, executable: p.exe}
	if code := run(context.Background(), []string{"tmux-binding"}, sys); code != 0 {
		t.Fatalf("tmux-binding = %d %s", code, errb.String())
	}
	binding := strings.TrimSpace(out.String())
	p.waitFor(t, "the empty state", func(s string) bool {
		return strings.Contains(s, "No agents yet — n new · a adopt a running claude pane") && strings.Contains(s, binding)
	})
	f.srv.Run(t, "send-keys", "-t", p.target, "q")
	if status := p.waitExit(t); status != "0" {
		t.Fatalf("exit status %s", status)
	}
}

// TestTUIUnopenableRegistryBanner: agentctl.db is a directory, so the
// registry cannot be opened. The TUI shows a one-line banner instead of
// crashing, q quits, and the command exits 1 printing the same line.
func TestTUIUnopenableRegistryBanner(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	home := testenv.TempHome(t)
	if err := os.MkdirAll(filepath.Join(home, "agentctl.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	env := f.env.With("AGENTCTL_HOME=" + home)
	p := tiStartTUI(t, f.srv, env, "ui", f.repo.Root, 160)
	p.waitFor(t, "the registry banner", func(s string) bool {
		return strings.Contains(s, "! cannot open registry "+filepath.Join(home, "agentctl.db"))
	})
	f.srv.Run(t, "send-keys", "-t", p.target, "j", "enter", "/") // ignored: there is nothing to act on
	f.srv.Run(t, "send-keys", "-t", p.target, "q")
	if status := p.waitExit(t); status != "1" {
		t.Fatalf("exit status %s, want 1\n%s", status, p.screen())
	}
	final, _ := f.srv.TryRun("capture-pane", "-p", "-J", "-S", "-200", "-t", p.target) // history + wrapped lines joined
	var lines []string
	for _, l := range strings.Split(final, "\n") {
		if strings.HasPrefix(l, "agentctl: ") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "agentctl: cannot open registry "+filepath.Join(home, "agentctl.db")+": ") {
		t.Fatalf("stderr lines after exit = %q\n%s", lines, final)
	}
}

func tiCountEvents(t *testing.T, env testenv.Env, typ string) int {
	t.Helper()
	p, _ := paths.Resolve(env.Getenv)
	st, err := store.Open(context.Background(), p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := st.Events().CountType(context.Background(), typ)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
