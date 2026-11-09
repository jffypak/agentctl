package tui

import (
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jffypak/agentctl/internal/controller"
)

// diffView is key d (list or detail): the task's changed files against its
// base, then enter pages the full diff (tracked changes plus untracked files
// as new-file diffs) through tea.ExecProcess and comes back to this view.
// Only git is read (plumbing, never the pane), so a disconnected task has a
// diff too. ui.action{diff} is recorded once the file list has been shown.
type diffView struct {
	id       int64
	sh       *shared
	task     controller.TaskView
	loaded   bool
	sum      controller.DiffSummary
	rows     []diffRow
	err      string // the summary could not be read
	note     string // the last pager problem
	recorded bool
	paging   bool // the full diff is being prepared or is on screen
	sc       scroll
	height   int // body rows, from the window size (a guess until it arrives)
}

// diffRow is one changed file.
type diffRow struct {
	path, added, deleted, note string
	conflict                   bool
}

type (
	diffLoadedMsg struct {
		id  int64
		sum controller.DiffSummary
		err error
	}
	// diffPagerMsg carries the prepared pager command (FullDiffCmd).
	diffPagerMsg struct {
		id     int64
		taskID string
		cmd    *exec.Cmd
		err    error
	}
	// diffPagerDoneMsg: the pager exited.
	diffPagerDoneMsg struct {
		id     int64
		taskID string
		err    error
	}
)

// diffHeaderRows: title, base line, headline, blank.
const diffHeaderRows = 4

func newDiffView(sh *shared, task controller.TaskView) (view, tea.Cmd) {
	v := diffView{id: nextViewID(), sh: sh, task: task, height: guessBodyRows}
	return v, tea.Batch(v.load(), tea.RequestWindowSize)
}

func (v diffView) load() tea.Cmd {
	sh, id, taskID := v.sh, v.id, v.task.ID
	return func() tea.Msg {
		sum, err := sh.ctl.DiffSummary(sh.ctx, taskID)
		return diffLoadedMsg{id: id, sum: sum, err: err}
	}
}

func (v diffView) Update(msg tea.Msg) (view, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.height = msg.Height - viewChrome
	case refreshedMsg:
		if t, ok := findTask(msg.ov, v.task.ID); ok {
			v.task = t
		}
	case diffLoadedMsg:
		if msg.id != v.id {
			return v, nil
		}
		v.loaded = true
		if msg.err != nil {
			v.err, v.rows = controller.OneLine(msg.err.Error()), nil
			return v, nil
		}
		v.err, v.sum, v.rows = "", msg.sum, diffRows(msg.sum)
		if !v.recorded {
			v.recorded = true
			return v, v.sh.actionCmd(controller.UIActionDiff, v.task.ID)
		}
	case diffPagerMsg:
		if msg.id != v.id {
			closeStdin(msg.cmd)
			return v, nil
		}
		if msg.err != nil {
			v.paging, v.note = false, controller.OneLine(msg.err.Error())
			return v, nil
		}
		c, id, taskID := msg.cmd, v.id, v.task.ID
		return v, tea.ExecProcess(c, func(err error) tea.Msg {
			closeStdin(c)
			return diffPagerDoneMsg{id: id, taskID: taskID, err: err}
		})
	case diffPagerDoneMsg:
		if msg.id != v.id {
			return v, nil
		}
		v.paging, v.note = false, ""
		if msg.err != nil {
			v.note = "pager: " + controller.OneLine(msg.err.Error())
		}
		return v, v.load() // the agent may have changed files meanwhile
	case tea.KeyPressMsg:
		return v.key(msg)
	}
	return v, nil
}

func (v diffView) key(k tea.KeyPressMsg) (view, tea.Cmd) {
	keys := v.sh.keys
	switch {
	case key.Matches(k, keys.Back, keys.Quit):
		return nil, nil
	case key.Matches(k, keys.Open):
		if !v.loaded || v.err != "" || v.paging || v.sum.Clean() {
			return v, nil
		}
		v.paging, v.note = true, ""
		sh, id, taskID := v.sh, v.id, v.task.ID
		return v, func() tea.Msg {
			c, err := sh.ctl.FullDiffCmd(sh.ctx, taskID)
			return diffPagerMsg{id: id, taskID: taskID, cmd: c, err: err}
		}
	}
	v.sc, _ = v.sc.key(k, keys, len(v.rows), v.visible(v.height))
	return v, nil
}

// visible is how many file rows fit in a body of h rows.
func (v diffView) visible(h int) int {
	n := max(h-diffHeaderRows, 1)
	if len(v.rows) > n { // one row for the "more" indicator
		n = max(n-1, 1)
	}
	return n
}

func (v diffView) Body(f frame) string {
	st := f.st
	lines := []string{viewTitle(f, "diff", v.task.Name, "")}
	switch {
	case !v.loaded:
		return strings.Join(append(lines, st.muted.Render("loading…")), "\n")
	case v.err != "":
		return strings.Join(append(lines, errorLines(f, v.err)...), "\n")
	}
	s := v.sum
	base := "vs " + controller.DisplayPath(s.BaseRef) + " @ " + shortHash(s.Base)
	if s.Head != "" {
		base += " · HEAD " + shortHash(s.Head)
	}
	lines = append(lines, st.muted.Render(truncate(base, f.width)), st.text.Render(truncate(diffHeadline(s), f.width)))
	switch {
	case v.note != "":
		lines = append(lines, errorLines(f, v.note)...)
	case v.paging:
		lines = append(lines, st.muted.Render("preparing the full diff…"))
	default:
		lines = append(lines, "")
	}
	if len(v.rows) == 0 {
		return strings.Join(lines, "\n")
	}
	vis := v.visible(f.height)
	start := v.sc.start(len(v.rows), vis)
	end := min(start+vis, len(v.rows))
	cols := diffColumns(v.rows, f.width)
	for _, r := range v.rows[start:end] {
		lines = append(lines, cols.render(st, r))
	}
	if more := moreLine(start, len(v.rows)-end); more != "" {
		lines = append(lines, st.muted.Render(more))
	}
	return strings.Join(lines, "\n")
}

func (v diffView) Hints() []key.Binding {
	k := v.sh.keys
	var h []key.Binding
	if v.loaded && v.err == "" && !v.sum.Clean() {
		h = append(h, help(k.Open, "full diff"))
	}
	if len(v.rows) > 0 {
		h = append(h, pairHint(k.Up, k.Down, "scroll"))
	}
	return append(h, help(k.Back, "back"))
}

// diffRows lists the net change against base: tracked files (renames,
// binaries, conflicts marked), then untracked files, then conflicted paths
// that have no tracked change of their own. Paths are display-quoted.
func diffRows(s controller.DiffSummary) []diffRow {
	conflicted := map[string]bool{}
	for _, p := range s.Conflicts {
		conflicted[p] = true
	}
	var rows []diffRow
	for _, c := range s.Total {
		r := diffRow{path: controller.DisplayPath(c.Path), added: "+" + strconv.Itoa(c.Added), deleted: "-" + strconv.Itoa(c.Deleted)}
		if c.OldPath != "" {
			r.path, r.note = controller.DisplayPath(c.OldPath)+" -> "+r.path, "renamed"
		}
		if c.Binary {
			r.added, r.deleted, r.note = "", "", "binary"
		}
		if conflicted[c.Path] {
			r.note, r.conflict = "conflict", true
			delete(conflicted, c.Path)
		}
		rows = append(rows, r)
	}
	for _, c := range s.Untracked {
		r := diffRow{path: controller.DisplayPath(c.Path), added: "+" + strconv.Itoa(c.Added), note: "untracked"}
		switch {
		case strings.HasSuffix(c.Path, "/"):
			r.added, r.note = "", "nested repository"
		case c.Binary:
			r.added, r.note = "", "binary · untracked"
		}
		rows = append(rows, r)
	}
	for _, p := range s.Conflicts {
		if conflicted[p] {
			rows = append(rows, diffRow{path: controller.DisplayPath(p), note: "conflict", conflict: true})
		}
	}
	return rows
}

// diffCols sizes the path / +added / -deleted / note columns.
type diffCols struct{ path, added, deleted int }

func diffColumns(rows []diffRow, width int) diffCols {
	var c diffCols
	note := 0
	for _, r := range rows {
		c.path = max(c.path, lipgloss.Width(r.path))
		c.added = max(c.added, len(r.added))
		c.deleted = max(c.deleted, len(r.deleted))
		note = max(note, lipgloss.Width(r.note))
	}
	fixed := gap + c.added + gap + c.deleted + gap + note
	c.path = max(min(c.path, width-fixed), min(c.path, minPathWidth))
	return c
}

// minPathWidth keeps paths readable on narrow screens (the note is cut first).
const minPathWidth = 16

func (c diffCols) render(st styles, r diffRow) string {
	sp := strings.Repeat(" ", gap)
	line := padRight(truncateLeft(r.path, c.path), c.path) + sp + padLeft(r.added, c.added) + sp + padLeft(r.deleted, c.deleted)
	noteStyle := st.muted
	if r.conflict {
		noteStyle = st.labelNeed
	}
	if r.note == "" {
		return st.text.Render(strings.TrimRight(line, " "))
	}
	return st.text.Render(line) + sp + noteStyle.Render(r.note)
}

func shortHash(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// --- shared by the manage views (diff, logs, new, adopt, close) ----------

// viewChrome is the rows the shell draws around a view body (header line
// and rule, footer rule and hints); guessBodyRows is the body height a view
// assumes until tea.RequestWindowSize answers.
const (
	viewChrome    = 4
	guessBodyRows = 24 - viewChrome
)

var viewIDs atomic.Int64

// nextViewID tags a view instance; its async results carry the tag, so a
// result for a closed view never lands in a newer one.
func nextViewID() int64 { return viewIDs.Add(1) }

// viewTitle: bold kind, the task name, then a muted suffix.
func viewTitle(f frame, kind, name, suffix string) string {
	st := f.st
	name, suffix = safeLine(name), safeLine(suffix)
	s := st.scope.Render(kind) + strings.Repeat(" ", gap) + st.nameSelected.Render(truncate(name, maxNameWidth))
	if suffix == "" {
		return s
	}
	used := lipgloss.Width(kind) + gap + lipgloss.Width(truncate(name, maxNameWidth))
	return s + st.muted.Render(truncate(" · "+suffix, f.width-used))
}

// pairHint shows two bindings as one footer hint ("↑/k ↓/j scroll").
func pairHint(a, b key.Binding, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(append(a.Keys(), b.Keys()...)...), key.WithHelp(a.Help().Key+" "+b.Help().Key, desc))
}

// textHint is a display-only footer hint.
func textHint(label, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(label), key.WithHelp(label, desc))
}

// moreLine says how many rows are above and below the window.
func moreLine(above, below int) string {
	var parts []string
	if above > 0 {
		parts = append(parts, "↑ "+strconv.Itoa(above)+" more")
	}
	if below > 0 {
		parts = append(parts, "↓ "+strconv.Itoa(below)+" more")
	}
	return strings.Join(parts, " · ")
}

// selectTask is the shell message that selects id in the list (and reloads).
func selectTask(id string) tea.Cmd {
	return func() tea.Msg { return selectTaskMsg{id: id} }
}

// closeStdin releases a pager command's input file (FullDiffCmd's unlinked
// temp file, PagerCmd's log).
func closeStdin(c *exec.Cmd) {
	if c == nil {
		return
	}
	if cl, ok := c.Stdin.(io.Closer); ok {
		_ = cl.Close()
	}
}

// scroll is a window over n rows: top is the first row shown; bottom pins
// the window to the end (the logs view follows the newest line).
type scroll struct {
	top    int
	bottom bool
}

func (s scroll) start(n, visible int) int {
	last := max(n-visible, 0)
	if s.bottom {
		return last
	}
	return min(max(s.top, 0), last)
}

func (s scroll) by(delta, n, visible int) scroll {
	last := max(n-visible, 0)
	top := min(max(s.start(n, visible)+delta, 0), last)
	return scroll{top: top, bottom: top == last}
}

// key applies a scrolling key: the up/down bindings move one row; pgup,
// pgdown, home and end (fixed navigation keys, not actions) move further.
func (s scroll) key(k tea.KeyPressMsg, keys KeyMap, n, visible int) (scroll, bool) {
	switch {
	case key.Matches(k, keys.Up):
		return s.by(-1, n, visible), true
	case key.Matches(k, keys.Down):
		return s.by(1, n, visible), true
	}
	switch k.String() {
	case "pgup":
		return s.by(-visible, n, visible), true
	case "pgdown":
		return s.by(visible, n, visible), true
	case "home":
		return scroll{}, true
	case "end":
		return scroll{bottom: true}, true
	}
	return s, false
}
