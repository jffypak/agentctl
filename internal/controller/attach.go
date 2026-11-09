package controller

import (
	"context"
	"errors"
	"os/exec"
	"strings"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Attach error kinds (in addition to ErrTaskNotFound and ErrTmuxMissing).
var (
	// ErrAttachRefused: the task has no verifiable pane (no session,
	// disconnected, identity mismatch, or the probe failed). Nothing was
	// switched or attached.
	ErrAttachRefused = errors.New("attach refused")
	// ErrAttachFailed: the pane was verified but tmux refused to switch.
	ErrAttachFailed = errors.New("attach failed")
)

// AttachResult says how an attach completes. Exactly one of Switched and Cmd
// is set.
type AttachResult struct {
	TaskID      string
	SessionName string
	PaneID      string
	// Switched: agentctl runs inside the agent's own tmux server, so the
	// user's client was moved to the pane with switch-client. The TUI exits
	// next (in a popup that lands the user on the agent).
	Switched bool
	// Cmd: agentctl runs outside that server. Cmd is an interactive
	// `tmux -S <socket> attach-session -t <pane>` with TMUX and TMUX_PANE
	// removed from its environment; the TUI runs it with tea.ExecProcess and
	// resumes when the user detaches.
	Cmd *exec.Cmd
}

// Attach focuses the task's agent pane (ARCHITECTURE.md §User flows, check
// in). Identity is verified first through liveSession (invariant 5): a
// session already recorded as disconnected, or one with no captured pane, is
// refused without any tmux call; a pane that is gone, reused (server or pane
// pid differs) or cannot be probed is refused after the read-only probe. Dead
// but verified panes (remain-on-exit) may be attached to read their output.
func (c *Controller) Attach(ctx context.Context, taskID string) (AttachResult, error) {
	sess, err := c.attachableSession(ctx, taskID)
	if err != nil {
		return AttachResult{}, err
	}
	live, pane := c.liveSession(ctx, sess)
	switch {
	case live.Status == domain.LivenessDisconnected:
		return AttachResult{}, userErr(ErrAttachRefused, nil,
			"attach refused: %s's pane is gone or was reused (%s)", taskID, live.Reason)
	case !live.IsVerified():
		return AttachResult{}, userErr(ErrAttachRefused, nil,
			"attach refused: cannot verify %s's pane (%s)", taskID, live.Detail)
	}
	res := AttachResult{TaskID: taskID, SessionName: pane.SessionName, PaneID: sess.Identity.PaneID}
	if res.SessionName == "" {
		res.SessionName = sess.SessionName
	}
	socket := sess.Identity.Socket
	if inside := tmux.SocketFromTMUX(c.Getenv("TMUX")); inside != "" && tmux.SameSocket(inside, socket) {
		if err := c.tmuxClient(socket).SwitchClient(ctx, sess.Identity.PaneID); err != nil {
			return AttachResult{}, userErr(ErrAttachFailed, err, "switch to %s failed: %s", taskID, err.Error())
		}
		res.Switched = true
		return res, nil
	}
	bin, err := c.lookPath("tmux")
	if err != nil {
		return AttachResult{}, userErr(ErrTmuxMissing, err, "tmux not found on PATH (install tmux 3.3 or newer)")
	}
	cmd, err := tmux.AttachCmd(bin, c.envWithout("TMUX", "TMUX_PANE"), tmux.Socket{Path: socket}, sess.Identity.PaneID)
	if err != nil {
		return AttachResult{}, userErr(ErrAttachFailed, err, "cannot attach to %s: %s", taskID, err.Error())
	}
	res.Cmd = cmd
	return res, nil
}

// attachableSession loads the task's newest session and refuses, before any
// tmux call, the cases where stored state already rules an attach out.
func (c *Controller) attachableSession(ctx context.Context, taskID string) (store.Session, error) {
	if _, err := c.store.Tasks().Get(ctx, taskID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.Session{}, userErr(ErrTaskNotFound, err, "task %q not found", taskID)
		}
		return store.Session{}, err
	}
	sess, err := c.store.Sessions().LatestForTask(ctx, taskID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return store.Session{}, userErr(ErrAttachRefused, nil, "attach refused: %s has no agent session", taskID)
	case err != nil:
		return store.Session{}, err
	case sess.State == domain.StateDisconnected:
		reason := strings.TrimPrefix(sess.LastDetail, "disconnected: ")
		if reason == "" {
			reason = "pane gone or reused"
		}
		return store.Session{}, userErr(ErrAttachRefused, nil, "attach refused: %s is disconnected (%s)", taskID, domain.SanitizeTerminalText(reason))
	case sess.Identity.IsZero():
		return store.Session{}, userErr(ErrAttachRefused, nil, "attach refused: %s has no recorded tmux pane", taskID)
	}
	return sess, nil
}

// envWithout returns the controller environment minus keys.
func (c *Controller) envWithout(keys ...string) []string {
	out := make([]string, 0, len(c.env))
	for _, kv := range c.env {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, d := range keys {
			drop = drop || k == d
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// TmuxBinding is the tmux.conf line that opens agentctlPath in a popup:
// `agentctl tmux-binding` prints it and the TUI empty state shows it.
func TmuxBinding(agentctlPath string) (string, error) {
	return tmux.PopupBinding(agentctlPath)
}
