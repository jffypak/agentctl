package controller

import (
	"context"
	"errors"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/tmux"
)

// ErrDockUnavailable: agentctl does not run in a tmux pane of the agent's
// own server, so there is no window to dock into. Callers fall back to
// Attach.
var ErrDockUnavailable = errors.New("dock unavailable")

// DockResult says what Dock did.
type DockResult struct {
	TaskID string
	PaneID string
	// Already: the agent was already docked in this window; it was focused.
	Already bool
}

// Dock shows the task's agent pane beside the pane agentctl runs in
// ($TMUX_PANE), in that pane's window, and focuses it (`agentctl tui
// --split`). See internal/tmux/dock.go for the swap mechanics.
//
// Identity is verified first, exactly as for Attach (invariant 5). The
// agent that was docked in this window before goes back to its own session
// in the same tmux invocation. An agent docked in another window is first
// sent home from there.
func (c *Controller) Dock(ctx context.Context, taskID string) (DockResult, error) {
	host := c.Getenv("TMUX_PANE")
	inside := tmux.SocketFromTMUX(c.Getenv("TMUX"))
	sess, err := c.attachableSession(ctx, taskID)
	if err != nil {
		return DockResult{}, err
	}
	if inside == "" || host == "" || !tmux.SameSocket(inside, sess.Identity.Socket) {
		return DockResult{}, ErrDockUnavailable
	}
	live, _ := c.liveSession(ctx, sess)
	switch {
	case live.Status == domain.LivenessDisconnected:
		return DockResult{}, userErr(ErrAttachRefused, nil,
			"attach refused: %s's pane is gone or was reused (%s)", taskID, live.Reason)
	case !live.IsVerified():
		return DockResult{}, userErr(ErrAttachRefused, nil,
			"attach refused: cannot verify %s's pane (%s)", taskID, live.Detail)
	}
	agent := sess.Identity.PaneID
	if agent == host {
		return DockResult{}, ErrDockUnavailable // agentctl runs in the agent's own pane
	}
	res := DockResult{TaskID: taskID, PaneID: agent}
	cl := c.tmuxClient(sess.Identity.Socket)
	fail := func(err error) (DockResult, error) {
		return DockResult{}, userErr(ErrAttachFailed, err, "cannot open %s beside the dashboard: %s", taskID, OneLine(err.Error()))
	}

	panes, err := cl.WindowPanes(ctx, host)
	if err != nil {
		return fail(err)
	}
	var docked, idle string // a pane docked here, and its slot or a free placeholder
	for _, p := range panes {
		switch {
		case p.PaneID == agent:
			if err := cl.SelectPane(ctx, agent); err != nil {
				return fail(err)
			}
			res.Already = true
			return res, nil
		case p.DockSlot != "" && docked == "":
			docked, idle = p.PaneID, p.DockSlot
		case p.Placeholder && idle == "":
			idle = p.PaneID
		}
	}

	var cmds [][]string
	// the agent may be docked beside another dashboard: send it home first
	if prev, err := cl.DockSlot(ctx, agent); err != nil {
		return fail(err)
	} else if prev != "" {
		cmds = append(cmds, tmux.UndockCommands(agent, prev)...)
	}
	if docked != "" {
		// its slot comes back into this window and becomes the agent's slot
		cmds = append(cmds, tmux.UndockCommands(docked, idle)...)
	}
	if idle == "" {
		idle, err = cl.SplitPlaceholder(ctx, host, placeholderText(sess.SessionName))
		if err != nil {
			return fail(err)
		}
	}
	cmds = append(cmds, tmux.DockInCommands(agent, idle)...)
	if err := cl.Dock(ctx, cmds); err != nil {
		return fail(err)
	}
	return res, nil
}

func placeholderText(session string) string {
	return "This agent is open beside the agentctl dashboard. It comes back here when you open another agent there or quit the dashboard. (" + domain.SanitizeTerminalText(session) + ")"
}

// Undock sends every agent docked in agentctl's own window back to its
// session and removes the placeholders left in that window. The TUI calls
// it when a --split dashboard exits. Not running in tmux is a no-op.
func (c *Controller) Undock(ctx context.Context) error {
	host := c.Getenv("TMUX_PANE")
	socket := tmux.SocketFromTMUX(c.Getenv("TMUX"))
	if socket == "" || host == "" {
		return nil
	}
	cl := c.tmuxClient(socket)
	panes, err := cl.WindowPanes(ctx, host)
	if err != nil {
		return err
	}
	var cmds [][]string
	var slots []string
	for _, p := range panes {
		switch {
		case p.DockSlot != "":
			cmds = append(cmds, tmux.UndockCommands(p.PaneID, p.DockSlot)...)
			slots = append(slots, p.DockSlot) // lands here after the swap
		case p.Placeholder:
			slots = append(slots, p.PaneID)
		}
	}
	if err := cl.Dock(ctx, cmds); err != nil {
		return err
	}
	var errs []error
	for _, s := range slots {
		errs = append(errs, cl.KillPlaceholder(ctx, s))
	}
	return errors.Join(errs...)
}

// undockPane sends id's pane home if it is docked anywhere (before close
// --stop kills its session, whose identity check needs the pane back in
// it). The placeholder it swaps with stays where the pane was, free for
// that dashboard's next dock.
func (c *Controller) undockPane(ctx context.Context, id domain.Identity) error {
	if id.IsZero() {
		return nil
	}
	cl := c.tmuxClient(id.Socket)
	slot, err := cl.DockSlot(ctx, id.PaneID)
	if err != nil || slot == "" {
		return err
	}
	return cl.Dock(ctx, tmux.UndockCommands(id.PaneID, slot))
}
