package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// composeView is key p: a multi-line prompt composer for one task. Every key
// goes to the text while it is open (q, n, j… are text; enter is a new
// line); ctrl+s (sh.keys.Send) sends through controller.Send in a tea.Cmd
// and esc cancels. Send pastes at once into a ready agent and otherwise
// queues; it never pastes into a working or awaiting-input agent. A refusal
// (stopped / failed / disconnected agent, invalid text) stays inline with
// the text kept. On success the composer closes, ui.action{prompt} is
// recorded and the message's state is flashed ("#12 queued · agent
// working"); the detail's messages section then follows it live.
type composeView struct {
	id   int64
	sh   *shared
	task controller.TaskView
	ta   textarea.Model
	busy bool
	err  string
}

// composeSentMsg is controller.Send's answer.
type composeSentMsg struct {
	id  int64
	res controller.SendResult
	err error
}

// composeChrome is the body rows around the text area (title, state line,
// blank, size line, error).
const composeChrome = 5

func newComposeView(sh *shared, task controller.TaskView) (view, tea.Cmd) {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.Placeholder = "type the prompt · enter adds a line"
	ta.KeyMap.Paste.SetEnabled(false) // terminal pastes arrive as tea.PasteMsg; never read the OS clipboard
	ta.SetStyles(composeStyles(newStyles(true, true)))
	_ = ta.Focus() // the blink command is dropped: the cursor never blinks
	return composeView{id: nextViewID(), sh: sh, task: task, ta: ta}, nil
}

// composeStyles: no colors, no cursor-line fill; the cursor is reverse video
// and does not blink (the TUI never animates; goldens stay deterministic).
func composeStyles(st styles) textarea.Styles {
	plain := textarea.StyleState{Placeholder: st.muted, EndOfBuffer: st.muted}
	s := textarea.Styles{Focused: plain, Blurred: plain}
	s.Cursor.Blink = false
	return s
}

func (v composeView) Update(msg tea.Msg) (view, tea.Cmd) {
	switch msg := msg.(type) {
	case composeSentMsg:
		if msg.id != v.id {
			return v, nil
		}
		return v.sent(msg)
	case refreshedMsg:
		if t, ok := findTask(msg.ov, v.task.ID); ok {
			v.task = t
		}
	case tea.PasteMsg:
		if !v.busy {
			v.ta, _ = v.ta.Update(msg)
			v.err = ""
		}
	case tea.KeyPressMsg:
		if v.busy {
			return v, nil // ctrl+c still quits (the shell handles it)
		}
		return v.key(msg)
	}
	return v, nil
}

func (v composeView) key(k tea.KeyPressMsg) (view, tea.Cmd) {
	switch {
	case key.Matches(k, v.sh.keys.Send):
		return v.submit()
	case k.String() == "esc" || (k.Text == "" && key.Matches(k, v.sh.keys.Back)):
		return nil, nil
	}
	v.ta, _ = v.ta.Update(k) // its cursor-blink commands are dropped
	v.err = ""
	return v, nil
}

func (v composeView) submit() (view, tea.Cmd) {
	body := v.ta.Value()
	if strings.TrimSpace(body) == "" {
		v.err = "type a prompt first"
		return v, nil
	}
	if err := domain.ValidateMessageBody(body); err != nil {
		v.err = err.Error()
		return v, nil
	}
	if v.sh.ctl == nil {
		return v, nil
	}
	v.busy, v.err = true, ""
	sh, id, req := v.sh, v.id, controller.SendRequest{TaskID: v.task.ID, Body: body, Source: controller.SendSourceTUI}
	return v, func() tea.Msg {
		res, err := sh.ctl.Send(sh.ctx, req)
		return composeSentMsg{id: id, res: res, err: err}
	}
}

func (v composeView) sent(msg composeSentMsg) (view, tea.Cmd) {
	v.busy = false
	if msg.err != nil {
		v.err = controller.OneLine(msg.err.Error())
		return v, nil
	}
	m := msg.res.Message
	note := "#" + strconv.FormatInt(m.ID, 10) + " " + m.Label
	flash := flashInfo(safeLine(note))
	if msg.res.DrainErr != "" {
		flash = func() tea.Msg {
			return flashMsg{text: safeLine(note + " · delivery attempt failed: " + msg.res.DrainErr), isErr: true}
		}
	}
	return nil, tea.Batch(v.sh.actionCmd(controller.UIActionPrompt, v.task.ID), func() tea.Msg { return reloadMsg{} }, flash)
}

// deliveryHint says what ctrl+s will do with the agent in its current state.
func deliveryHint(t controller.TaskView) (string, bool) {
	switch {
	case !t.HasSession:
		return "no agent session yet · send will be refused", true
	case domain.SendRejected(t.AgentState):
		return "agent " + stateWord(t.AgentState) + " · send will be refused", true
	}
	if why := domain.QueueLabel(t.AgentState, t.Confidence, false); why != "" {
		return "will be queued · " + why, t.AgentState == domain.StateUnknown || t.AgentState == domain.StateAwaitingInput
	}
	return "ready · delivered when you send", false
}

func (v composeView) Body(f frame) string {
	st := f.st
	hint, warn := deliveryHint(v.task)
	hintStyle := st.muted
	if warn {
		hintStyle = st.labelNeed
	}
	lines := []string{viewTitle(f, "prompt", v.task.Name, ""), hintStyle.Render(truncate(hint, f.width)), ""}
	ta := v.ta
	ta.SetStyles(composeStyles(st))
	ta.SetWidth(max(f.width, 1))
	ta.SetHeight(max(f.height-composeChrome, 1))
	lines = append(lines, ta.View(), v.sizeLine(f))
	switch {
	case v.busy:
		lines = append(lines, st.muted.Render(truncate("sending…", f.width)))
	case v.err != "":
		lines = append(lines, errorLines(f, v.err)...)
	}
	return strings.Join(lines, "\n")
}

// sizeLine: `2 lines · 41 bytes`, in the error color past the limit.
func (v composeView) sizeLine(f frame) string {
	body := v.ta.Value()
	n := len(body)
	text := plural(strings.Count(body, "\n")+1, "line") + " · " + plural(n, "byte")
	style := f.st.muted
	if n > domain.MaxMessageBytes {
		style = f.st.errText
		text += " · over the " + strconv.Itoa(domain.MaxMessageBytes/1024) + " KiB limit"
	}
	return style.Render(truncate(text, f.width))
}

func (v composeView) Hints() []key.Binding {
	k := v.sh.keys
	if v.busy {
		return []key.Binding{k.ForceQuit}
	}
	return []key.Binding{help(k.Send, "send"), textHint("↵", "new line"), help(k.Back, "cancel")}
}
