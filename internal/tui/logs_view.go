package tui

import (
	"os/exec"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// logsView is key l (list or detail): the pane's recent output, scrollable,
// newest at the bottom. It is a snapshot (no streaming, spec §4.2). The
// capture goes through controller.Inspect, which verifies the pane identity
// first, so a gone or reused pane shows "pane unavailable" and nothing from
// it is ever captured. sh.keys.FullCapture writes the whole scrollback to
// logs/<task>/<UTC>.log and pages it with tea.ExecProcess, then returns here.
// ui.action{logs} is recorded once output has been shown.
type logsView struct {
	id        int64
	sh        *shared
	task      controller.TaskView
	loaded    bool
	in        controller.Inspection
	err       string // Inspect failed (unknown task, registry error)
	note      string // the last full-capture outcome
	noteErr   bool
	savedTo   string // the last full capture's path
	recorded  bool
	capturing bool
	sc        scroll
	height    int
}

type (
	logsLoadedMsg struct {
		id  int64
		in  controller.Inspection
		err error
	}
	// captureMsg carries the full capture's log path and its pager command.
	captureMsg struct {
		id     int64
		taskID string
		path   string
		cmd    *exec.Cmd
		err    error
	}
	// capturePagerDoneMsg: the pager showing the capture exited.
	capturePagerDoneMsg struct {
		id     int64
		taskID string
		path   string
		err    error
	}
)

const (
	// logsLines bounds the recent capture (Inspect reads 2000 lines of
	// history; f pages everything).
	logsLines = 2000
	// logsHeaderRows: title, info line, blank.
	logsHeaderRows = 3
	savedPrefix    = "full capture saved to "
)

func newLogsView(sh *shared, task controller.TaskView) (view, tea.Cmd) {
	v := logsView{id: nextViewID(), sh: sh, task: task, sc: scroll{bottom: true}, height: guessBodyRows}
	sh2, id, taskID := sh, v.id, task.ID
	load := func() tea.Msg {
		in, err := sh2.ctl.Inspect(sh2.ctx, taskID, controller.InspectOptions{Lines: logsLines})
		return logsLoadedMsg{id: id, in: in, err: err}
	}
	return v, tea.Batch(load, tea.RequestWindowSize)
}

func (v logsView) Update(msg tea.Msg) (view, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.height = msg.Height - viewChrome
	case refreshedMsg:
		if t, ok := findTask(msg.ov, v.task.ID); ok {
			v.task = t
		}
	case logsLoadedMsg:
		if msg.id != v.id {
			return v, nil
		}
		v.loaded, v.in, v.err = true, msg.in, ""
		if msg.err != nil {
			v.err = controller.OneLine(msg.err.Error())
			return v, nil
		}
		if msg.in.Pane.Available && !v.recorded {
			v.recorded = true
			return v, v.sh.actionCmd(controller.UIActionLogs, v.task.ID)
		}
	case captureMsg:
		if msg.id != v.id {
			closeStdin(msg.cmd)
			return v, nil
		}
		if msg.err != nil {
			v.capturing, v.note, v.noteErr = false, controller.OneLine(msg.err.Error()), true
			return v, nil
		}
		c, id, taskID, path := msg.cmd, v.id, v.task.ID, msg.path
		return v, tea.ExecProcess(c, func(err error) tea.Msg {
			closeStdin(c)
			return capturePagerDoneMsg{id: id, taskID: taskID, path: path, err: err}
		})
	case capturePagerDoneMsg:
		if msg.id != v.id {
			return v, nil
		}
		v.capturing, v.note, v.noteErr, v.savedTo = false, savedPrefix+msg.path, false, msg.path
		if msg.err != nil {
			v.note, v.noteErr, v.savedTo = "pager: "+controller.OneLine(msg.err.Error())+" (capture saved to "+msg.path+")", true, ""
		}
	case tea.KeyPressMsg:
		return v.key(msg)
	}
	return v, nil
}

func (v logsView) key(k tea.KeyPressMsg) (view, tea.Cmd) {
	keys := v.sh.keys
	switch {
	case key.Matches(k, keys.Back, keys.Quit):
		return nil, nil
	case key.Matches(k, keys.FullCapture):
		if !v.loaded || v.capturing {
			return v, nil
		}
		v.capturing, v.note, v.noteErr, v.savedTo = true, "", false, ""
		sh, id, taskID := v.sh, v.id, v.task.ID
		return v, func() tea.Msg {
			path, err := sh.ctl.CaptureFull(sh.ctx, taskID)
			if err != nil {
				return captureMsg{id: id, taskID: taskID, err: err}
			}
			c, err := sh.ctl.PagerCmd(path)
			return captureMsg{id: id, taskID: taskID, path: path, cmd: c, err: err}
		}
	}
	v.sc, _ = v.sc.key(k, keys, len(v.in.Output), max(v.height-logsHeaderRows, 1))
	return v, nil
}

func (v logsView) Body(f frame) string {
	st := f.st
	in := v.in
	switch {
	case !v.loaded:
		return viewTitle(f, "logs", v.task.Name, "") + "\n" + st.muted.Render("loading…")
	case v.err != "":
		return strings.Join(append([]string{viewTitle(f, "logs", v.task.Name, "")}, errorLines(f, v.err)...), "\n")
	case !in.Pane.Available:
		lines := []string{viewTitle(f, "logs", v.task.Name, ""), st.unknown.Render(truncate(in.Pane.Note, f.width)),
			st.muted.Render(truncate("nothing is captured from a pane whose identity cannot be verified", f.width))}
		if v.note != "" {
			lines = append(lines, "", v.noteLine(f))
		}
		return strings.Join(lines, "\n")
	}
	lines := []string{viewTitle(f, "logs", v.task.Name, in.Pane.Note)}
	switch {
	case v.note != "":
		lines = append(lines, v.noteLine(f))
	case v.capturing:
		lines = append(lines, st.muted.Render("writing the full capture…"))
	default:
		info := "last " + plural(len(in.Output), "line") + " · newest at the bottom"
		if len(in.Output) == 0 {
			info = "no output yet"
		}
		lines = append(lines, st.muted.Render(truncate(info, f.width)))
	}
	lines = append(lines, "")
	vis := max(f.height-logsHeaderRows, 1)
	start := v.sc.start(len(in.Output), vis)
	for _, l := range in.Output[start:min(start+vis, len(in.Output))] {
		lines = append(lines, st.text.Render(truncate(displayLine(l), f.width)))
	}
	return strings.Join(lines, "\n")
}

func (v logsView) noteLine(f frame) string {
	if v.noteErr {
		return strings.Join(errorLines(f, v.note), "\n")
	}
	if v.savedTo == "" {
		return f.st.muted.Render(truncate(v.note, f.width))
	}
	// a long path keeps its end (the task directory and the file name)
	return f.st.muted.Render(savedPrefix + truncateLeft(v.savedTo, max(f.width-len(savedPrefix), 1)))
}

func (v logsView) Hints() []key.Binding {
	k := v.sh.keys
	h := []key.Binding{}
	if v.loaded && v.err == "" {
		h = append(h, k.FullCapture)
	}
	if n := len(v.in.Output); n > 0 {
		h = append(h, pairHint(k.Up, k.Down, "scroll"))
	}
	return append(h, help(k.Back, "back"))
}
