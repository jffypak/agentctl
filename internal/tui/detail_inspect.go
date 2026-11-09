package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// inspectSection is the detail view's diff summary and last output lines
// (spec §4.3: changed files and +/- summary, access to raw output). It is
// loaded by a tea.Cmd running controller.Inspect (identity is verified before
// any capture; a disconnected or reused pane still gets its diff). Until the
// result arrives View shows "loading…"; Update only stores results and never
// blocks. It re-inspects when the task's state changes.
type inspectSection struct {
	sh       *shared
	task     controller.TaskView
	seq      int       // id of the newest request; older answers are dropped
	inFlight bool      // a request is running
	sentAt   time.Time // when it was sent
	stale    bool      // the task changed while a request was running
	loaded   bool
	in       controller.Inspection
	err      string // Inspect itself failed (unknown task, registry error)
}

// inspectedMsg is one Inspect result for the detail section.
type inspectedMsg struct {
	taskID string
	seq    int
	in     controller.Inspection
	err    error
}

const (
	// inspectLines is how many recent output lines the detail shows at most.
	inspectLines = 10
	// detailFieldRows estimates the detail rows above this section (title
	// through socket) so the newest output lines are the ones that fit: the
	// shell clips the body at the bottom.
	detailFieldRows = 13
	// minOutputRows is shown however small the terminal.
	minOutputRows = 2
	// inspectRetry: the shell hands results to the view on top, so an answer
	// that arrived while e.g. the diff view was open is lost; a request older
	// than this is sent again on the next refresh.
	inspectRetry = 2 * time.Second
)

func newInspectSection(sh *shared, task controller.TaskView) (inspectSection, tea.Cmd) {
	return inspectSection{sh: sh, task: task}.load()
}

// load starts an Inspect in a tea.Cmd.
func (s inspectSection) load() (inspectSection, tea.Cmd) {
	if s.sh == nil || s.sh.ctl == nil {
		return s, nil
	}
	s.seq++
	s.inFlight, s.stale, s.sentAt = true, false, time.Now()
	sh, id, seq := s.sh, s.task.ID, s.seq
	return s, func() tea.Msg {
		in, err := sh.ctl.Inspect(sh.ctx, id, controller.InspectOptions{Lines: inspectLines})
		return inspectedMsg{taskID: id, seq: seq, in: in, err: err}
	}
}

func (s inspectSection) Update(msg tea.Msg) (inspectSection, tea.Cmd) {
	switch msg := msg.(type) {
	case inspectedMsg:
		if msg.taskID != s.task.ID || msg.seq != s.seq {
			return s, nil
		}
		s.inFlight, s.loaded, s.in, s.err = false, true, msg.in, ""
		if msg.err != nil {
			s.err = controller.OneLine(msg.err.Error())
		}
		if s.stale {
			return s.load()
		}
	case refreshedMsg:
		v, ok := findTask(msg.ov, s.task.ID)
		if !ok {
			return s, nil
		}
		changed := inspectKeyOf(v) != inspectKeyOf(s.task)
		s.task = v
		switch {
		case s.inFlight && time.Since(s.sentAt) > inspectRetry:
			return s.load()
		case changed && s.inFlight:
			s.stale = true
		case changed:
			return s.load()
		}
	}
	return s, nil
}

// inspectKey is what makes a fresh Inspect worthwhile: the agent moved on.
type inspectKey struct {
	task   domain.TaskState
	state  domain.AgentState
	since  int64
	detail string
}

func inspectKeyOf(v controller.TaskView) inspectKey {
	return inspectKey{task: v.TaskState, state: v.AgentState, since: v.StateSince.UnixMilli(), detail: v.LastDetail}
}

// View renders the `diff` and `output` rows under the detail fields.
func (s inspectSection) View(f frame) string {
	if s.sh == nil || s.sh.ctl == nil {
		return ""
	}
	st := f.st
	if !s.loaded {
		return labeled(f, "diff", "loading…", st.muted) + "\n" + labeled(f, "output", "loading…", st.muted)
	}
	if s.err != "" {
		return labeled(f, "inspect", "! "+safeLine(s.err), st.errText)
	}
	lines := []string{s.diffRow(f)}
	in := s.in
	switch {
	case !in.Pane.Available:
		lines = append(lines, labeled(f, "output", safeLine(in.Pane.Note), st.unknown))
	case len(in.Output) == 0:
		lines = append(lines, labeled(f, "output", safeLine(in.Pane.Note)+" · no output yet", st.muted))
	default:
		lines = append(lines, labeled(f, "output", safeLine(in.Pane.Note), st.muted))
		n := min(len(in.Output), inspectLines, max(f.height-detailFieldRows-2, minOutputRows))
		for _, l := range in.Output[len(in.Output)-n:] {
			lines = append(lines, labeled(f, "", displayLine(l), st.text))
		}
	}
	return strings.Join(lines, "\n")
}

func (s inspectSection) diffRow(f frame) string {
	if s.in.DiffErr != "" {
		return labeled(f, "diff", "! "+safeLine(s.in.DiffErr), f.st.errText)
	}
	return labeled(f, "diff", diffHeadline(s.in.Diff), f.st.text)
}

// labeled is fieldLine with a styled value (truncated before styling, so an
// escape sequence is never cut).
func labeled(f frame, label, value string, style lipgloss.Style) string {
	return f.st.muted.Render(padRight(label, fieldLabelWidth)) + style.Render(truncate(value, f.width-fieldLabelWidth))
}

// diffHeadline is the one-line change summary shared by the detail and the
// diff view: `2 files +4 -1 · 1 untracked · 1 conflict · 3 commits`.
func diffHeadline(s controller.DiffSummary) string {
	if s.Clean() {
		return "no changes vs base"
	}
	var parts []string
	if len(s.Total) > 0 {
		added, deleted := 0, 0
		for _, c := range s.Total {
			added += c.Added
			deleted += c.Deleted
		}
		parts = append(parts, fmt.Sprintf("%s +%d -%d", plural(len(s.Total), "file"), added, deleted))
	}
	if n := len(s.Untracked); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" untracked")
	}
	if n := len(s.Conflicts); n > 0 {
		parts = append(parts, plural(n, "conflict"))
	}
	if s.Commits > 0 {
		parts = append(parts, plural(s.Commits, "commit"))
	}
	if len(parts) == 0 { // e.g. only an empty commit
		return "no file changes vs base"
	}
	return strings.Join(parts, " · ")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// tabWidth is the tab stop used when showing captured output.
const tabWidth = 8

// displayLine prepares one captured line (already sanitized by the
// controller) for a fixed-width row: sanitized again (cheap and idempotent),
// tabs expanded to spaces so widths stay right.
func displayLine(s string) string {
	s = domain.SanitizeTerminalText(s)
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - col%tabWidth
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += lipgloss.Width(string(r))
	}
	return b.String()
}
