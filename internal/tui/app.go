// Package tui is agentctl's Bubble Tea v2 interface: an attention-sorted
// list of tasks, a detail view, and attach/focus. It is presentation only
// (invariant 1): it holds a *controller.Controller, reads through it, acts
// through it, and never opens the registry itself. Exiting it never touches
// an agent (invariant 2).
package tui

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// Options configure a TUI run.
type Options struct {
	// Controller is the only way the TUI reads or changes state. Nil means
	// the registry could not be opened: the TUI shows OpenErr as a banner
	// and can only quit.
	Controller *controller.Controller
	OpenErr    error
	// Cwd scopes the list to the repo containing it (all repos outside one).
	Cwd string
	// Binding is the `agentctl tmux-binding` line shown in the empty state.
	Binding string
	// Keys overrides the default keymap (config.yaml keys, m2-tui-manage).
	Keys *KeyMap
	// Warning is a one-line startup warning shown as a banner (e.g. a
	// config.yaml problem, m2-tui-manage).
	Warning string
	// NoColor drops every color (NO_COLOR); bold and faint remain.
	NoColor bool
	// RunID is the ui_run_id actions are recorded under. Run sets it from
	// Controller.UIOpened; it stays empty in tests that do not record.
	RunID string
	// Input, Output and Environ are the terminal and environment Run uses
	// (nil: the process's). TMUX and TMUX_PANE are removed from the
	// environment Bubble Tea sees, so its color detection never runs
	// `tmux info` against an unqualified server.
	Input   io.Reader
	Output  io.Writer
	Environ []string
}

type mode int

const (
	modeList mode = iota
	modeDetail
)

// Model is the root Bubble Tea model. It is a value: Update returns a
// changed copy and never blocks (all controller work runs in tea.Cmds).
type Model struct {
	sh      *shared
	noColor bool
	dark    bool
	st      styles

	width, height int

	ov       controller.Overview
	loaded   bool
	lastID   int64     // newest event id seen
	lastLoad time.Time // wall clock of the last applied Overview
	pollSeq  int       // increments per launched poll; stale results are dropped
	applied  int       // seq of the Overview on screen

	selected string // selected task id (stable across refreshes)
	filter   filterState
	mode     mode
	detail   detailModel
	views    []view // pushed views, top last

	openErr, loadErr, preflightErr, reconcileErr, warning string
	flash                                                 flashMsg
}

// New builds the model. ctx is the program's context; every controller call
// uses it.
func New(ctx context.Context, opts Options) Model {
	keys := DefaultKeyMap()
	if opts.Keys != nil {
		keys = *opts.Keys
	}
	m := Model{
		sh: &shared{
			ctx: ctx, ctl: opts.Controller, keys: keys, runID: opts.RunID, cwd: opts.Cwd, binding: opts.Binding,
		},
		noColor: opts.NoColor, dark: true, st: newStyles(true, opts.NoColor),
		width: 80, height: 24,
		warning: controller.OneLine(opts.Warning),
	}
	if opts.Controller == nil {
		err := opts.OpenErr
		if err == nil {
			err = errors.New("no registry")
		}
		m.openErr = controller.OneLine(err.Error())
	}
	return m
}

// Run records ui.opened, runs the program until the user quits or ctx is
// cancelled (cmd_tui cancels it on SIGHUP), then records ui.closed. A
// cancelled context is a clean exit, not an error.
//
// Cancellation is turned into a graceful Quit rather than handed to Bubble
// Tea with tea.WithContext: v2.1.1's kill path closes the input
// cancelreader while its reader goroutine is still using the file
// descriptor (a data race the race detector reports), the quit path does not.
func Run(ctx context.Context, opts Options, progOpts ...tea.ProgramOption) error {
	ctl := opts.Controller
	if ctl != nil && opts.RunID == "" {
		run, err := ctl.UIOpened(ctx)
		if err != nil {
			opts.Warning = "could not record ui.opened: " + err.Error()
		}
		opts.RunID = run
	}
	var base []tea.ProgramOption
	if opts.Input != nil {
		base = append(base, tea.WithInput(opts.Input))
	}
	if opts.Output != nil {
		base = append(base, tea.WithOutput(opts.Output))
	}
	if opts.Environ != nil {
		base = append(base, tea.WithEnvironment(withoutTmux(opts.Environ)))
	}
	p := tea.NewProgram(New(ctx, opts), append(base, progOpts...)...)
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			p.Quit()
		case <-finished:
		}
	}()
	_, err := p.Run()
	close(finished)
	if ctl != nil && opts.RunID != "" {
		closeErr := ctl.UIClosed(context.WithoutCancel(ctx), opts.RunID)
		if err == nil {
			err = closeErr
		}
	}
	if errors.Is(err, tea.ErrInterrupted) {
		return nil // SIGINT on a non-tty: same as ctrl+c
	}
	return err
}

// withoutTmux drops TMUX and TMUX_PANE from env.
func withoutTmux(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			out = append(out, kv)
		}
	}
	return out
}

// Init starts the first load, the pane reconcile loop, the preflight check
// (tmux / git / agent on PATH) and the background-color query.
func (m Model) Init() tea.Cmd {
	if m.sh.ctl == nil {
		return tea.RequestBackgroundColor
	}
	return tea.Batch(
		tea.RequestBackgroundColor,
		wrapPoll(loadCmd(m.sh), 0, true),
		preflightCmd(m.sh),
		tick(m.sh.ctl.Tunables().Reconcile, reconcileTickMsg{}),
	)
}

// seqPollMsg is a poll result tagged with its launch sequence number (stale
// results never overwrite newer ones) and whether it belongs to the tick loop
// (exactly one loop runs, however many reloads are requested).
type seqPollMsg struct {
	seq   int
	chain bool
	polledMsg
}

func wrapPoll(c tea.Cmd, seq int, chain bool) tea.Cmd {
	return func() tea.Msg {
		msg := c()
		if p, ok := msg.(polledMsg); ok {
			return seqPollMsg{seq: seq, chain: chain, polledMsg: p}
		}
		return msg
	}
}

func (m Model) launchPoll(c tea.Cmd, chain bool) (Model, tea.Cmd) {
	m.pollSeq++
	return m, wrapPoll(c, m.pollSeq, chain)
}

// Update handles one message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m.forward(msg)
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.st = newStyles(m.dark, m.noColor)
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.PasteMsg:
		if m.filter.editing && len(m.views) == 0 {
			m.filter.input, _ = m.filter.input.Update(msg)
			return m, nil
		}
		return m.forward(msg)
	case polledMsg:
		return m.applyPoll(seqPollMsg{seq: m.pollSeq, polledMsg: msg})
	case seqPollMsg:
		return m.applyPoll(msg)
	case pollTickMsg:
		if m.sh.ctl == nil {
			return m, nil
		}
		return m.launchPoll(pollCmd(m.sh, m.lastID, time.Since(m.lastLoad) >= ageRefresh), true)
	case reconcileTickMsg:
		if m.sh.ctl == nil {
			return m, nil
		}
		return m, reconcileCmd(m.sh)
	case reconciledMsg:
		m.reconcileErr = ""
		if msg.err != nil {
			m.reconcileErr = "reconcile: " + controller.OneLine(msg.err.Error())
		}
		return m, tick(m.sh.ctl.Tunables().Reconcile, reconcileTickMsg{})
	case preflightMsg:
		m.preflightErr = ""
		if msg.err != nil {
			m.preflightErr = controller.OneLine(msg.err.Error())
		}
		return m, nil
	case flashMsg:
		m.flash = msg
		return m, nil
	case reloadMsg:
		if m.sh.ctl == nil {
			return m, nil
		}
		return m.launchPoll(pollCmd(m.sh, m.lastID, true), false)
	case selectTaskMsg:
		m.selected, m.mode = msg.id, modeList
		if m.sh.ctl == nil {
			return m, nil
		}
		return m.launchPoll(pollCmd(m.sh, m.lastID, true), false)
	case attachMsg:
		return m.applyAttach(msg)
	case execDoneMsg:
		if msg.err != nil {
			m.flash = flashMsg{text: "tmux attach: " + controller.OneLine(msg.err.Error()), isErr: true}
		}
		return m.launchPoll(pollCmd(m.sh, m.lastID, true), false)
	}
	return m.forward(msg)
}

// applyPoll stores a poll result and broadcasts the fresh Overview.
func (m Model) applyPoll(p seqPollMsg) (tea.Model, tea.Cmd) {
	var next tea.Cmd
	if p.chain {
		next = tick(m.sh.ctl.Tunables().Poll, pollTickMsg{})
	}
	m.lastID = max(m.lastID, p.lastID)
	if p.err != nil {
		m.loadErr = "cannot read the registry: " + controller.OneLine(p.err.Error())
		return m, next
	}
	m.loadErr = ""
	if p.ov == nil || p.seq < m.applied {
		return m, next
	}
	m.ov, m.loaded, m.applied, m.lastLoad = *p.ov, true, p.seq, time.Now()
	m2, cmd := m.forward(refreshedMsg{ov: m.ov})
	return m2, tea.Batch(next, cmd)
}

func (m Model) applyAttach(a attachMsg) (tea.Model, tea.Cmd) {
	switch {
	case a.err != nil:
		m.flash = flashMsg{text: controller.OneLine(a.err.Error()), isErr: true}
		return m, nil
	case a.res.Switched:
		return m, tea.Quit // the user's client is on the agent now; a popup closes
	case a.res.Cmd != nil:
		if a.warn != "" {
			m.flash = flashMsg{text: a.warn, isErr: true}
		}
		return m, tea.ExecProcess(a.res.Cmd, func(err error) tea.Msg { return execDoneMsg{err: err} })
	}
	return m, nil
}

// forward hands msg to the top view, else to the detail (and its sections).
// Input (keys, pastes) goes to the top view only; everything else, such as
// refreshedMsg and async results, also reaches the detail underneath, so an
// answer meant for a detail section is not lost while a view is open.
func (m Model) forward(msg tea.Msg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	if len(m.views) > 0 {
		var c tea.Cmd
		m, c = m.updateTop(msg)
		cmds = append(cmds, c)
		if isInput(msg) {
			return m, tea.Batch(cmds...)
		}
	}
	if m.mode == modeDetail {
		var c tea.Cmd
		m.detail, c = m.detail.Update(msg)
		cmds = append(cmds, c)
	}
	return m, tea.Batch(cmds...)
}

// isInput reports whether msg is user input, which only the top view gets.
func isInput(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.KeyReleaseMsg, tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg,
		tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.MouseMotionMsg:
		return true
	}
	return false
}

// updateTop updates the top view, popping it when it returns nil.
func (m Model) updateTop(msg tea.Msg) (Model, tea.Cmd) {
	top := len(m.views) - 1
	next, cmd := m.views[top].Update(msg)
	views := slices.Clone(m.views[:top])
	if next != nil {
		views = append(views, next)
	}
	m.views = views
	return m, cmd
}

// push shows v (a nil v keeps the current screen).
func (m Model) push(v view, cmd tea.Cmd) (Model, tea.Cmd) {
	if v != nil {
		m.views = append(slices.Clone(m.views), v)
	}
	return m, cmd
}

func (m Model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keys := m.sh.keys
	m.flash = flashMsg{}
	if key.Matches(k, keys.ForceQuit) {
		return m, tea.Quit
	}
	if m.sh.ctl == nil { // registry unopenable: the banner explains; only quitting works
		if key.Matches(k, keys.Quit, keys.Back) {
			return m, tea.Quit
		}
		return m, nil
	}
	switch {
	case len(m.views) > 0:
		return m.updateTop(k)
	case m.filter.editing:
		return m.filterKey(k)
	case m.mode == modeDetail:
		return m.detailKey(k)
	}
	return m.listKey(k)
}

func (m Model) listKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keys := m.sh.keys
	task, hasTask := m.selectedTask()
	switch {
	case key.Matches(k, keys.Up):
		return m.move(-1), nil
	case key.Matches(k, keys.Down):
		return m.move(1), nil
	case key.Matches(k, keys.Open):
		if !hasTask {
			return m, nil
		}
		m.mode, m.selected = modeDetail, task.ID
		var cmd tea.Cmd
		m.detail, cmd = newDetail(m.sh, task)
		return m, cmd
	case key.Matches(k, keys.Filter):
		m.filter.editing = true
		return m, nil
	case key.Matches(k, keys.Back):
		if m.filter.query() != "" {
			m.filter = filterState{}
			return m, nil
		}
		return m, tea.Quit
	case key.Matches(k, keys.Quit):
		return m, tea.Quit
	case key.Matches(k, keys.New):
		return m.push(newNewTaskView(m.sh))
	case key.Matches(k, keys.Adopt):
		return m.push(newAdoptView(m.sh))
	}
	if hasTask {
		return m.taskKey(k, task)
	}
	return m, nil
}

// taskKey handles the per-task view keys shared by the list and the detail.
func (m Model) taskKey(k tea.KeyPressMsg, task controller.TaskView) (Model, tea.Cmd) {
	keys := m.sh.keys
	switch {
	case key.Matches(k, keys.Prompt):
		return m.push(newComposeView(m.sh, task))
	case key.Matches(k, keys.Diff):
		return m.push(newDiffView(m.sh, task))
	case key.Matches(k, keys.Logs):
		return m.push(newLogsView(m.sh, task))
	case key.Matches(k, keys.Close):
		return m.push(newCloseConfirmView(m.sh, task))
	}
	return m, nil
}

func (m Model) detailKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keys := m.sh.keys
	switch {
	case key.Matches(k, keys.Open):
		return m, attachCmd(m.sh, m.detail.task.ID)
	case key.Matches(k, keys.Back, keys.Quit):
		m.mode = modeList
		return m, nil
	case key.Matches(k, keys.Prompt, keys.Diff, keys.Logs, keys.Close):
		return m.taskKey(k, m.detail.task)
	}
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(k)
	return m, cmd
}

func (m Model) filterKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	keys := m.sh.keys
	switch {
	case key.Matches(k, keys.Back):
		m.filter = filterState{}
		return m, nil
	case key.Matches(k, keys.Open):
		m.filter.editing = false
		return m, nil
	case k.String() == "up":
		return m.move(-1), nil
	case k.String() == "down":
		return m.move(1), nil
	}
	m.filter.input, _ = m.filter.input.Update(k)
	return m, nil
}

// rows are the tasks currently listed (filter applied).
func (m Model) rows() []controller.TaskView {
	return visibleRows(m.ov.Rows, m.filter.query())
}

// selectedTask is the highlighted row: the remembered id when still listed,
// else the first row.
func (m Model) selectedTask() (controller.TaskView, bool) {
	rows := m.rows()
	if len(rows) == 0 {
		return controller.TaskView{}, false
	}
	return rows[max(indexOf(rows, m.selected), 0)], true
}

func (m Model) move(delta int) Model {
	rows := m.rows()
	if len(rows) == 0 {
		return m
	}
	i := max(indexOf(rows, m.selected), 0) + delta
	m.selected = rows[min(max(i, 0), len(rows)-1)].ID
	return m
}

func indexOf(rows []controller.TaskView, id string) int {
	for i, r := range rows {
		if r.ID == id {
			return i
		}
	}
	return -1
}

// findTask looks a task up in an Overview.
func findTask(ov controller.Overview, id string) (controller.TaskView, bool) {
	for _, r := range ov.Rows {
		if r.ID == id {
			return r, true
		}
	}
	return controller.TaskView{}, false
}

// View renders the full screen in the alternate buffer.
func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}
