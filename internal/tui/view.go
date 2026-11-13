package tui

import (
	"context"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// This file is the contract between the app shell (m2-tui-inbox) and the
// views later cards fill in (compose.go, detail_messages.go: m2-prompt-queue;
// diff_view.go, logs_view.go, newtask_view.go, adopt_view.go,
// confirm_view.go, detail_inspect.go: m2-tui-manage). See
// docs/notes/m2-tui-inbox.md for the full contract.

// shared is what every view needs. New builds it once; nothing mutates it.
type shared struct {
	ctx     context.Context // the program's context: pass it to every controller call
	ctl     *controller.Controller
	keys    KeyMap
	runID   string // ui_run_id of this TUI run ("" in tests that do not record)
	cwd     string // scopes the list and NewTask
	binding string // `agentctl tmux-binding` line (empty state)
	stay    bool   // Options.Stay: keep running after a switch-client attach
}

// frame is the space and styling a view renders into.
type frame struct {
	width, height int
	st            styles
}

// view is a full-body screen pushed over the list or detail (composer, diff,
// logs, new task, adopt picker, confirm). The shell keeps drawing the header,
// banner and footer; the view owns the body. While a view is on top it gets
// every message first (all keys except ctrl+c, window sizes, refreshedMsg and
// its own async results); text fields therefore see q, n, j... as text.
type view interface {
	// Update handles one message. It returns the next view, or nil to close
	// it (the list or detail underneath shows again).
	Update(msg tea.Msg) (view, tea.Cmd)
	// Body renders the view into f.width x f.height cells.
	Body(f frame) string
	// Hints are the footer key hints while the view is on top.
	Hints() []key.Binding
}

// Messages the shell understands. Views and sections return them from
// commands; the shell also broadcasts refreshedMsg.
type (
	// refreshedMsg carries every fresh Overview to the top view and to the
	// detail sections, so they can follow live state.
	refreshedMsg struct{ ov controller.Overview }

	// flashMsg shows one transient line above the footer until the next key.
	flashMsg struct {
		text  string
		isErr bool
	}

	// reloadMsg asks the shell to re-read the Overview now (after an action
	// changed state, e.g. close or adopt).
	reloadMsg struct{}

	// selectTaskMsg selects task id in the list (and reloads), e.g. after
	// NewTask succeeds so the new row shows selected as starting.
	selectTaskMsg struct{ id string }
)

// flashInfo returns a command showing an informational one-liner.
func flashInfo(text string) tea.Cmd {
	return func() tea.Msg { return flashMsg{text: text} }
}

// flashError returns a command showing err as a one-line error.
func flashError(err error) tea.Cmd {
	return func() tea.Msg { return flashMsg{text: controller.OneLine(err.Error()), isErr: true} }
}

// comingIn is what a stubbed key shows until its card lands.
func comingIn(feature, issue string) tea.Cmd {
	return flashInfo(feature + ": coming in " + issue)
}

// actionCmd records ui.action{action, task_id} for this run when the user's
// action has actually happened (a prompt queued, a diff opened, a task
// created...). Errors surface as a flash; nothing is recorded without a run.
func (sh *shared) actionCmd(action, taskID string) tea.Cmd {
	if sh.ctl == nil || sh.runID == "" {
		return nil
	}
	return func() tea.Msg {
		if err := sh.ctl.UIAction(sh.ctx, sh.runID, action, taskID); err != nil {
			return flashMsg{text: "could not record ui.action: " + controller.OneLine(err.Error()), isErr: true}
		}
		return nil
	}
}
