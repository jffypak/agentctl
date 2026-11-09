package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// SwitchClientCommand moves the current client to target. A pane id ("%N")
// also selects that pane's window and makes the pane active, so the user
// lands exactly on the agent.
func SwitchClientCommand(target string) []string {
	return []string{"switch-client", "-t", target}
}

// SwitchClient runs switch-client on the client's socket. Without -c, tmux
// picks the invoking terminal's client ($TMUX_PANE, else the most recently
// active client of the most recently active session).
func (c Client) SwitchClient(ctx context.Context, target string) error {
	if target == "" {
		return errors.New("tmux: switch-client needs a target")
	}
	_, err := c.run(ctx, nil, SwitchClientCommand(target))
	return err
}

// AttachSessionCommand attaches a new client to target (a pane id selects
// the pane, like switch-client).
func AttachSessionCommand(target string) []string {
	return []string{"attach-session", "-t", target}
}

// AttachCmd builds the interactive `tmux -S <socket> attach-session -t
// <target>` the TUI hands to tea.ExecProcess when it is not running inside the
// target server. env is the child's complete environment (nil inherits the
// process environment); callers remove TMUX
// and TMUX_PANE so tmux does not refuse to nest.
func AttachCmd(bin string, env []string, s Socket, target string) (*exec.Cmd, error) {
	if target == "" {
		return nil, errors.New("tmux: attach needs a target")
	}
	return InteractiveCmd(bin, env, s, nil, AttachSessionCommand(target))
}

// InteractiveCmd builds an *exec.Cmd for a long-lived tmux client (an attach
// that owns the terminal, or a `-C` control-mode client in tests). Unlike
// Runner invocations it is not run here: the caller wires stdio and starts it.
// clientFlags go before the socket flag (e.g. "-C"); they may not name a
// socket, which always comes from s.
func InteractiveCmd(bin string, env []string, s Socket, clientFlags, command []string) (*exec.Cmd, error) {
	if bin == "" || !filepath.IsAbs(bin) {
		return nil, fmt.Errorf("tmux: binary path %q is not absolute", bin)
	}
	for _, f := range clientFlags {
		if !strings.HasPrefix(f, "-") || f == "-L" || f == "-S" || f == "-f" {
			return nil, fmt.Errorf("tmux: invalid client flag %q", f)
		}
	}
	argv, err := Argv(Request{Socket: s, Commands: [][]string{command}}, "")
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, append(append([]string(nil), clientFlags...), argv...)...)
	cmd.Env = append([]string(nil), env...)
	return cmd, nil
}
