package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// detailModel is the task detail (spec §4.3, Phase-1 subset): identity,
// state with its confidence and age, why it needs you, then the prompt-queue
// section (m2-prompt-queue) and the diff/output section (m2-tui-manage).
// enter attaches; it follows live state through refreshedMsg.
type detailModel struct {
	sh       *shared
	task     controller.TaskView
	messages messagesSection
	inspect  inspectSection
}

func newDetail(sh *shared, task controller.TaskView) (detailModel, tea.Cmd) {
	msgs, c1 := newMessagesSection(sh, task)
	ins, c2 := newInspectSection(sh, task)
	return detailModel{sh: sh, task: task, messages: msgs, inspect: ins}, tea.Batch(c1, c2)
}

// Update follows refreshes and hands everything to the sections (keys the
// shell did not consume included, e.g. the cancel-message key).
func (d detailModel) Update(msg tea.Msg) (detailModel, tea.Cmd) {
	if r, ok := msg.(refreshedMsg); ok {
		if v, found := findTask(r.ov, d.task.ID); found {
			d.task = v
		}
	}
	var c1, c2 tea.Cmd
	d.messages, c1 = d.messages.Update(msg)
	d.inspect, c2 = d.inspect.Update(msg)
	return d, tea.Batch(c1, c2)
}

// maxGoalLines bounds the wrapped goal.
const maxGoalLines = 3

// fieldLabelWidth is the label column of detail fields ("needs you" + gap).
const fieldLabelWidth = 11

// fieldLine renders one `label  value` detail row clipped to the frame.
func fieldLine(f frame, label, value string) string {
	return f.st.muted.Render(padRight(label, fieldLabelWidth)) + truncate(value, f.width-fieldLabelWidth)
}

// Body renders the detail.
func (d detailModel) Body(f frame) string {
	t, st := d.task, f.st
	valueW := max(f.width-fieldLabelWidth, 1)
	lines := []string{d.title(f), ""}
	goal := wrapWords(oneLine(domain.SanitizeTerminalText(t.Goal)), valueW)
	if len(goal) > maxGoalLines { // the wrap is greedy, so the joined tail always overflows and gets "…"
		tail := truncate(strings.Join(goal[maxGoalLines-1:], " "), valueW)
		goal = append(goal[:maxGoalLines-1:maxGoalLines-1], tail)
	}
	for i, l := range goal {
		label := ""
		if i == 0 {
			label = "goal"
		}
		lines = append(lines, fieldLine(f, label, l))
	}
	if t.Goal == "" {
		lines = append(lines, fieldLine(f, "goal", "—"))
	}
	last := oneLine(domain.SanitizeTerminalText(t.LastDetail))
	if last == "" {
		last = "—"
	}
	lines = append(lines, fieldLine(f, "last", last), d.stateLine(f))
	if t.Attention.Section == domain.SectionNeedsYou {
		lines = append(lines, st.muted.Render(padRight("needs you", fieldLabelWidth))+
			st.labelNeed.Render(truncate(t.Attention.Label, valueW)))
	}
	agent := "—"
	if t.HasSession {
		agent = t.AgentType + " · " + string(t.Origin)
		if t.Model != "" {
			agent += " · " + safeLine(t.Model)
		}
	}
	base := t.BaseRef
	if sha := shortSHA(t.BaseSHA); sha != "" {
		base += " @ " + sha
	}
	lines = append(lines,
		fieldLine(f, "agent", agent),
		st.muted.Render(padRight("worktree", fieldLabelWidth))+truncateLeft(domain.SanitizeTerminalText(t.Worktree), valueW),
		fieldLine(f, "branch", t.Branch),
		fieldLine(f, "base", strings.TrimSpace(base)),
		fieldLine(f, "tmux", tmuxIdentity(t)),
	)
	if t.HasSession && t.Identity.Socket != "" {
		lines = append(lines, st.muted.Render(padRight("socket", fieldLabelWidth))+st.muted.Render(truncateLeft(t.Identity.Socket, valueW)))
	}
	lines = append(lines, d.textLines(f)...)
	for _, section := range []string{d.messages.View(f), d.inspect.View(f)} {
		if section != "" {
			lines = append(lines, section)
		}
	}
	return strings.Join(lines, "\n")
}

// Detail text geometry: the hook text is context here (the full output is
// one key away), so it is kept short.
const (
	maxReplyLines  = 6
	maxPromptLines = 3
)

// textLines: the agent's last message and the last prompt (wrapped, short),
// only when hooks reported them.
func (d detailModel) textLines(f frame) []string {
	t, st := d.task, f.st
	valueW := max(f.width-fieldLabelWidth, 1)
	var lines []string
	add := func(label, text string, n int) {
		for i, l := range clipRows(wrapText(stripMarkdown(text), valueW), n, valueW) {
			if i > 0 {
				label = ""
			}
			lines = append(lines, st.muted.Render(padRight(label, fieldLabelWidth))+l)
		}
	}
	if t.LastMessage != "" {
		label := "reply"
		if t.Unread {
			label = unreadDot + " reply"
		}
		add(label, t.LastMessage, maxReplyLines)
	}
	if t.LastPrompt != "" {
		add("prompt", t.LastPrompt, maxPromptLines)
	}
	return lines
}

// title is the task name and its attention section + label.
func (d detailModel) title(f frame) string {
	t, st := d.task, f.st
	sec := t.Attention.Section
	label := sec.Title() + " · " + t.Attention.Label
	style := st.text
	switch sec {
	case domain.SectionNeedsYou:
		style = st.labelNeed
	case domain.SectionInitial, domain.SectionUnknown, domain.SectionClosed:
		style = st.unknown
	}
	name := truncate(t.Name, maxNameWidth)
	return st.nameSelected.Render(name) + strings.Repeat(" ", gap) + style.Render(truncate(label, f.width-lipgloss.Width(name)-gap))
}

// stateLine: `state  <word> · confidence <c> · since <age>`. Unknown is
// spelled unknown (never ready / idle / done) and stopped is "exited".
func (d detailModel) stateLine(f frame) string {
	t := d.task
	if !t.HasSession {
		return fieldLine(f, "state", "no session yet · since "+formatAge(t.Age))
	}
	value := stateWord(t.AgentState) + " · confidence " + string(t.Confidence) + " · since " + formatAge(t.Age)
	if t.Attention.Section == domain.SectionUnknown || t.Attention.Section == domain.SectionInitial {
		return f.st.muted.Render(padRight("state", fieldLabelWidth)) + f.st.unknown.Render(truncate(value, f.width-fieldLabelWidth))
	}
	return fieldLine(f, "state", value)
}

// stateWord is how an agent state reads to a person.
func stateWord(s domain.AgentState) string {
	switch s {
	case domain.StateAwaitingInput:
		return "awaiting input"
	case domain.StateStopped:
		return "exited"
	case "":
		return "no session"
	}
	return string(s)
}

func tmuxIdentity(t controller.TaskView) string {
	if !t.HasSession || t.Identity.IsZero() {
		return "no pane recorded"
	}
	s := t.SessionName
	if s == "" {
		s = "?"
	}
	return s + " · " + t.Identity.SessionID + " " + t.Identity.PaneID + " · pane pid " + strconv.Itoa(t.Identity.PanePID)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
