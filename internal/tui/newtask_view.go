package tui

import (
	"errors"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// newTaskView is key n: a name + goal form. The name is checked as it is
// typed (domain.ValidateSlug). Submitting runs controller.NewTask in a
// tea.Cmd with Cwd sh.cwd (worktree ../<repo>-worktrees/<name>, branch
// agentctl/<name>, agent in tmux session agentctl-<name>); keys are ignored
// until it answers. Every refusal (branch exists, not a repo, agent missing,
// …) is shown inline with both fields kept. On success the form closes and
// the new row is selected (starting), and ui.action{new} is recorded.
//
// Text fields see keys before the keymap: q, n, j… are text. Besides the
// open/back bindings, enter and esc always submit/cancel here, and tab,
// shift+tab, ↑ and ↓ move between the fields.
type newTaskView struct {
	id         int64
	sh         *shared
	name, goal lineInput
	focus      int // focusName or focusGoal
	busy       bool
	err        string // the controller's refusal
}

const (
	focusName = iota
	focusGoal
)

// newTaskDoneMsg is NewTask's answer.
type newTaskDoneMsg struct {
	id  int64
	res controller.NewTaskResult
	err error
}

// formLabelWidth is the form's label column ("name   ", "goal   ").
const formLabelWidth = 7

func newNewTaskView(sh *shared) (view, tea.Cmd) {
	return newTaskView{
		id: nextViewID(), sh: sh,
		name: lineInput{limit: domain.MaxSlugLen},
		goal: lineInput{limit: controller.MaxGoalLen / utf8.UTFMax},
	}, nil
}

func (v newTaskView) Update(msg tea.Msg) (view, tea.Cmd) {
	switch msg := msg.(type) {
	case newTaskDoneMsg:
		if msg.id != v.id {
			return v, nil
		}
		return v.done(msg)
	case tea.PasteMsg:
		if !v.busy {
			v = v.edit(msg)
		}
	case tea.KeyPressMsg:
		if v.busy {
			return v, nil // ctrl+c still quits (the shell handles it)
		}
		return v.key(msg)
	}
	return v, nil
}

func (v newTaskView) key(k tea.KeyPressMsg) (view, tea.Cmd) {
	keys := v.sh.keys
	s := k.String()
	switch {
	case s == "esc" || (k.Text == "" && key.Matches(k, keys.Back)):
		return nil, nil
	case s == "tab" || s == "shift+tab":
		v.focus = 1 - v.focus
		return v, nil
	case s == "up":
		v.focus = focusName
		return v, nil
	case s == "down":
		v.focus = focusGoal
		return v, nil
	case s == "enter" || (k.Text == "" && key.Matches(k, keys.Open)):
		if v.focus == focusName {
			if slugProblem(v.name.Value()) == "" {
				v.focus = focusGoal
			}
			return v, nil
		}
		return v.submit()
	}
	return v.edit(k), nil
}

// edit sends a key or paste to the focused field.
func (v newTaskView) edit(msg tea.Msg) newTaskView {
	var handled bool
	if v.focus == focusName {
		v.name, handled = v.name.Update(msg)
	} else {
		v.goal, handled = v.goal.Update(msg)
	}
	if handled {
		v.err = "" // the refusal was about what was there before
	}
	return v
}

func (v newTaskView) submit() (view, tea.Cmd) {
	if slugProblem(v.name.Value()) != "" {
		v.focus = focusName
		return v, nil
	}
	v.busy, v.err = true, ""
	sh, id := v.sh, v.id
	req := controller.NewTaskRequest{Name: v.name.Value(), Goal: v.goal.Value(), Cwd: sh.cwd}
	return v, func() tea.Msg {
		res, err := sh.ctl.NewTask(sh.ctx, req)
		return newTaskDoneMsg{id: id, res: res, err: err}
	}
}

func (v newTaskView) done(msg newTaskDoneMsg) (view, tea.Cmd) {
	v.busy = false
	id := msg.res.TaskID
	switch {
	case msg.err == nil:
		return nil, tea.Batch(v.sh.actionCmd(controller.UIActionNew, id), selectTask(id),
			flashInfo(safeLine("started "+id+" · branch "+msg.res.Branch+" · "+msg.res.Worktree)))
	case id != "": // created, but the agent failed to start: the row shows it as failed
		return nil, tea.Batch(v.sh.actionCmd(controller.UIActionNew, id), selectTask(id), flashErr(msg.err))
	}
	v.err = controller.OneLine(msg.err.Error())
	switch {
	case errors.Is(msg.err, controller.ErrInvalidGoal):
		v.focus = focusGoal
	case errors.Is(msg.err, controller.ErrInvalidName), errors.Is(msg.err, controller.ErrTaskExists),
		errors.Is(msg.err, controller.ErrBranchExists), errors.Is(msg.err, controller.ErrWorktreeExists),
		errors.Is(msg.err, controller.ErrSessionExists):
		v.focus = focusName
	}
	return v, nil
}

// slugProblem is the inline name check ("" when valid or still empty is
// reported as its own hint by the view).
func slugProblem(name string) string {
	err := domain.ValidateSlug(name)
	if err == nil {
		return ""
	}
	return strings.TrimPrefix(err.Error(), domain.ErrInvalidSlug.Error()+": ")
}

func (v newTaskView) Body(f frame) string {
	st := f.st
	lines := []string{
		st.scope.Render("new task"),
		st.muted.Render(truncate("a worktree and branch from the current HEAD, with the agent started in tmux", f.width)),
		"",
		formField(f, "name", v.name, v.focus == focusName && !v.busy),
		v.nameHint(f),
		formField(f, "goal", v.goal, v.focus == focusGoal && !v.busy),
		formHint(f, "the agent's first prompt (optional)", st.muted),
		"",
	}
	switch {
	case v.busy:
		lines = append(lines, st.muted.Render(truncate("creating "+v.name.Value()+"…", f.width)))
	case v.err != "":
		lines = append(lines, errorLines(f, v.err)...)
	}
	return strings.Join(lines, "\n")
}

func (v newTaskView) nameHint(f frame) string {
	name := v.name.Value()
	switch {
	case name == "":
		return formHint(f, "1-40 lowercase letters, digits or '-'", f.st.muted)
	case slugProblem(name) != "":
		return slugHint(f, name)
	}
	return formHint(f, "creates branch "+controller.BranchPrefix+name, f.st.muted)
}

func (v newTaskView) Hints() []key.Binding {
	k := v.sh.keys
	if v.busy {
		return []key.Binding{k.ForceQuit}
	}
	next := help(k.Open, "next")
	if v.focus == focusGoal {
		next = help(k.Open, "create")
	}
	return []key.Binding{next, textHint("tab", "switch field"), help(k.Back, "cancel")}
}

// formField renders `label  <input>` (focused label bold, cursor shown).
func formField(f frame, label string, in lineInput, focused bool) string {
	style := f.st.muted
	if focused {
		style = f.st.hintKey
	}
	return style.Render(padRight(label, formLabelWidth)) + in.View(f.st, max(f.width-formLabelWidth, 1), focused)
}

// slugHint is the inline name error, wrapped to two lines under the field.
func slugHint(f frame, name string) string {
	w := max(f.width-formLabelWidth, 1)
	ls := wrapWords("! "+slugProblem(name), w)
	if len(ls) > 2 {
		ls = append(ls[:1:1], truncate(strings.Join(ls[1:], " "), w))
	}
	for i, l := range ls {
		ls[i] = strings.Repeat(" ", formLabelWidth) + f.st.errText.Render(l)
	}
	return strings.Join(ls, "\n")
}

// formHint renders a muted (or error) line under a field, in its column.
func formHint(f frame, text string, style lipgloss.Style) string {
	return strings.Repeat(" ", formLabelWidth) + style.Render(truncate(text, f.width-formLabelWidth))
}

// maxErrorLines bounds a wrapped inline error: actionable refusals (a path,
// the fix) are long, and cutting them at the width would drop the fix.
const maxErrorLines = 3

// errorLines renders "! msg" wrapped to the width, at most maxErrorLines.
func errorLines(f frame, msg string) []string {
	ls := wrapWords("! "+safeLine(msg), max(f.width, 1))
	if len(ls) > maxErrorLines {
		tail := truncate(strings.Join(ls[maxErrorLines-1:], " "), f.width)
		ls = append(ls[:maxErrorLines-1:maxErrorLines-1], tail)
	}
	for i, l := range ls {
		ls[i] = f.st.errText.Render(l)
	}
	return ls
}

// safeLine is text from outside the TUI (errors with paths, tmux names,
// config snippets) made safe for one terminal line: escape sequences and
// control characters stripped, whitespace collapsed.
func safeLine(s string) string { return controller.OneLine(domain.SanitizeTerminalText(s)) }

// flashErr is flashError with the text sanitized.
func flashErr(err error) tea.Cmd {
	return func() tea.Msg { return flashMsg{text: safeLine(err.Error()), isErr: true} }
}
