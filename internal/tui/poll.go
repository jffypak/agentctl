package tui

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// ageRefresh bounds how stale displayed ages get when no event arrives.
const ageRefresh = time.Second

// Messages produced by the shell's own commands.
type (
	pollTickMsg      struct{}
	reconcileTickMsg struct{}

	// polledMsg is one poll: the newest event id seen and, when something
	// changed (or ages needed a refresh), a fresh Overview.
	polledMsg struct {
		lastID int64
		ov     *controller.Overview
		err    error
	}
	reconciledMsg struct{ err error }
	preflightMsg  struct{ err error }

	// attachMsg is the controller's verdict on an attach.
	attachMsg struct {
		res  controller.AttachResult
		err  error
		warn string // the attach happened but ui.action could not be recorded
		// returnKey: after a --stay switch, the user's tmux key for
		// switch-client -l ("" if none or the lookup failed)
		returnKey string
		// docked: --split opened the agent beside the TUI (res is unset)
		docked *controller.DockResult
	}
	// execDoneMsg: the interactive tmux attach returned (the user detached).
	execDoneMsg struct{ err error }
)

// loadCmd is the first read: the newest event id, then the Overview.
func loadCmd(sh *shared) tea.Cmd {
	return func() tea.Msg {
		last, err := sh.ctl.LastEventID(sh.ctx)
		if err != nil {
			return polledMsg{err: err}
		}
		ov, err := overview(sh)
		if err != nil {
			return polledMsg{lastID: last, err: err}
		}
		return polledMsg{lastID: last, ov: &ov}
	}
}

// pollCmd reads the events appended after `after` (by hooks, `agentctl`
// commands, other TUIs...) and reloads the Overview when any arrived or when
// force is set.
func pollCmd(sh *shared, after int64, force bool) tea.Cmd {
	return func() tea.Msg {
		evs, err := sh.ctl.EventsSince(sh.ctx, after, 0)
		if err != nil {
			return polledMsg{lastID: after, err: err}
		}
		last := after
		if n := len(evs); n > 0 {
			last = evs[n-1].ID
		}
		if len(evs) == 0 && !force {
			return polledMsg{lastID: last}
		}
		ov, err := overview(sh)
		if err != nil {
			return polledMsg{lastID: after, err: err}
		}
		return polledMsg{lastID: last, ov: &ov}
	}
}

// overview always includes closed tasks; the filter decides what shows.
func overview(sh *shared) (controller.Overview, error) {
	return sh.ctl.Overview(sh.ctx, controller.OverviewQuery{Cwd: sh.cwd, IncludeClosed: true})
}

// reconcileCmd verifies panes and drains deliverable messages (both are
// stubs until m1-pane-identity / m2-prompt-queue merge).
func reconcileCmd(sh *shared) tea.Cmd {
	return func() tea.Msg {
		return reconciledMsg{err: errors.Join(sh.ctl.Reconcile(sh.ctx), sh.ctl.DrainAll(sh.ctx))}
	}
}

func preflightCmd(sh *shared) tea.Cmd {
	return func() tea.Msg { return preflightMsg{err: sh.ctl.Preflight(sh.ctx)} }
}

func tick(d time.Duration, msg tea.Msg) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return msg })
}

// attachCmd asks the controller to attach and records ui.action{attach}
// once the attach is going to happen (switched, or about to exec).
func attachCmd(sh *shared, taskID string) tea.Cmd {
	return func() tea.Msg {
		if sh.split {
			d, err := sh.ctl.Dock(sh.ctx, taskID)
			switch {
			case err == nil:
				return recordAttach(sh, taskID, attachMsg{docked: &d})
			case !errors.Is(err, controller.ErrDockUnavailable):
				return attachMsg{err: err}
			} // not in the agent's tmux server: attach as usual
		}
		res, err := sh.ctl.Attach(sh.ctx, taskID)
		if err != nil {
			return attachMsg{err: err}
		}
		out := attachMsg{res: res}
		if res.Switched && sh.stay {
			out.returnKey, _ = sh.ctl.ReturnKey(sh.ctx) // a failed lookup only loses the key from the hint
		}
		return recordAttach(sh, taskID, out)
	}
}

// recordAttach records ui.action{attach} for an attach that happened, and
// that the user looked at the task (best effort: the attach happened).
func recordAttach(sh *shared, taskID string, out attachMsg) attachMsg {
	_ = sh.ctl.MarkViewed(sh.ctx, taskID)
	if sh.runID != "" {
		if err := sh.ctl.UIAction(sh.ctx, sh.runID, controller.UIActionAttach, taskID); err != nil {
			out.warn = "could not record ui.action: " + controller.OneLine(err.Error())
		}
	}
	return out
}
