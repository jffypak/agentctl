package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// confirmView is key x: "close <task>? y/N". Only sh.keys.Confirm says yes;
// every other key (enter and esc included) is No and closes the dialog
// without touching anything (spec §4.5: no key silently performs a
// destructive action). A plain close only hides the task — worktree,
// branch and tmux session are left alone (invariants 2 and 9).
//
// "close and stop" (kill the agent's tmux session) is offered only when
// controller.PlanClose(Stop: true) accepts it — a session agentctl launched
// whose pane identity verifies right now — and is a second question with
// its own y/N. On success the dialog closes, ui.action{close} is recorded,
// and the list is shown (the closed row disappears from it).
type confirmView struct {
	id      int64
	sh      *shared
	task    controller.TaskView
	stage   int // askClose or askStop
	planned bool
	stopOK  bool
	stop    controller.ClosePlan // valid when stopOK
	stopErr string               // why close-and-stop is not offered
	busy    bool
}

const (
	askClose = iota
	askStop
)

type (
	// closePlanMsg: whether close-and-stop may be offered.
	closePlanMsg struct {
		id      int64
		stop    controller.ClosePlan
		stopErr error
	}
	closeDoneMsg struct {
		id  int64
		res controller.CloseResult
		err error
	}
)

func newCloseConfirmView(sh *shared, task controller.TaskView) (view, tea.Cmd) {
	if task.TaskState == domain.TaskClosed {
		return nil, flashInfo(task.Name + " is already closed")
	}
	v := confirmView{id: nextViewID(), sh: sh, task: task}
	id, name := v.id, task.ID
	return v, func() tea.Msg {
		plan, err := sh.ctl.PlanClose(sh.ctx, controller.CloseRequest{Name: name, Stop: true})
		return closePlanMsg{id: id, stop: plan, stopErr: err}
	}
}

func (v confirmView) Update(msg tea.Msg) (view, tea.Cmd) {
	switch msg := msg.(type) {
	case closePlanMsg:
		if msg.id != v.id {
			return v, nil
		}
		v.planned, v.stopOK, v.stop, v.stopErr = true, msg.stopErr == nil, msg.stop, ""
		if msg.stopErr != nil {
			v.stopErr = safeLine(msg.stopErr.Error())
		}
	case closeDoneMsg:
		if msg.id != v.id {
			return v, nil
		}
		if msg.err != nil {
			return nil, flashErr(msg.err)
		}
		done := "closed " + v.task.ID + " · its worktree, branch and agent are untouched (/closed lists it)"
		if msg.res.Stopped {
			done = "stopped and closed " + v.task.ID + " · its worktree and branch are kept"
		}
		return nil, tea.Batch(v.sh.actionCmd(controller.UIActionClose, v.task.ID), selectTask(v.task.ID), flashInfo(done))
	case tea.KeyPressMsg:
		return v.key(msg)
	}
	return v, nil
}

func (v confirmView) key(k tea.KeyPressMsg) (view, tea.Cmd) {
	if v.busy {
		return v, nil
	}
	keys := v.sh.keys
	switch {
	case key.Matches(k, keys.Confirm):
		return v.close(v.stage == askStop)
	case v.stage == askClose && v.stopOK && key.Matches(k, keys.Close):
		v.stage = askStop
		return v, nil
	}
	return nil, nil // No
}

// close runs CloseTask in a tea.Cmd. stop is only ever true after the second
// explicit confirmation; CloseTask re-validates it.
func (v confirmView) close(stop bool) (view, tea.Cmd) {
	v.busy = true
	sh, id := v.sh, v.id
	req := controller.CloseRequest{Name: v.task.ID, Stop: stop}
	return v, func() tea.Msg {
		res, err := sh.ctl.CloseTask(sh.ctx, req)
		return closeDoneMsg{id: id, res: res, err: err}
	}
}

func (v confirmView) Body(f frame) string {
	if v.stage == askStop {
		return v.stopBody(f)
	}
	st, t := f.st, v.task
	lines := []string{st.sectionNeed.Render(truncate("close "+t.Name+"? y/N", f.width)), ""}
	lines = append(lines, bullet(f, "hides it from the list (/closed shows it again); nothing is deleted")...)
	lines = append(lines, bullet(f, "keeps worktree "+domain.SanitizeTerminalText(t.Worktree))...)
	lines = append(lines, bullet(f, "keeps branch "+safeLine(t.Branch))...)
	if t.HasSession && t.SessionName != "" {
		session := controller.DisplayPath(t.SessionName)
		if t.AgentState.IsTerminal() {
			lines = append(lines, bullet(f, "leaves tmux session "+session+" as it is")...)
		} else {
			lines = append(lines, bullet(f, "the agent keeps running in tmux session "+session)...)
		}
	}
	lines = append(lines, "")
	switch {
	case v.busy:
		lines = append(lines, st.muted.Render("closing…"))
	case v.stopOK:
		lines = append(lines, st.muted.Render(truncate(v.sh.keys.Close.Help().Key+" close and stop… kills the agent's tmux session (asks again)", f.width)))
	case v.stopErr != "" && t.Origin == domain.OriginLaunched && t.HasSession:
		lines = append(lines, st.muted.Render(truncate("close and stop unavailable: "+v.stopErr, f.width)))
	}
	return strings.Join(lines, "\n")
}

func (v confirmView) stopBody(f frame) string {
	st, t, p := f.st, v.task, v.stop
	session := controller.DisplayPath(p.SessionName)
	lines := []string{st.sectionNeed.Render(truncate("stop and close "+t.Name+"? y/N", f.width)), ""}
	lines = append(lines, bullet(f, "kills tmux session "+session+" (pane "+p.Identity.PaneID+", pid "+strconv.Itoa(p.Identity.PanePID)+"): the agent stops now")...)
	lines = append(lines, bullet(f, "then closes the task; worktree and branch "+safeLine(t.Branch)+" are kept")...)
	if v.busy {
		lines = append(lines, "", st.muted.Render("stopping…"))
	}
	return strings.Join(lines, "\n")
}

func (v confirmView) Hints() []key.Binding {
	k := v.sh.keys
	if v.busy {
		return []key.Binding{k.ForceQuit}
	}
	if v.stage == askStop {
		return []key.Binding{help(k.Confirm, "stop and close"), textHint("other keys", "cancel")}
	}
	h := []key.Binding{help(k.Confirm, "close")}
	if v.stopOK {
		h = append(h, help(k.Close, "close and stop…"))
	}
	return append(h, textHint("other keys", "cancel"))
}

// bulletIndent indents explanation lines under a question.
const bulletIndent = "  "

// bullet is an explanation line, wrapped (at most two lines) so a long path
// keeps its end.
func bullet(f frame, text string) []string {
	ls := wrapWords(text, max(f.width-len(bulletIndent), 1))
	if len(ls) > 2 {
		ls = append(ls[:1:1], truncateLeft(strings.Join(ls[1:], " "), f.width-len(bulletIndent)))
	}
	for i, l := range ls {
		ls[i] = bulletIndent + f.st.text.Render(l)
	}
	return ls
}
