package tui

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// adoptView is key a: a picker over live panes that no task owns
// (controller.ListAdoptCandidates; by default only panes running a known
// agent command, sh.keys.Adopt toggles every pane). Nothing is inferred
// (invariant 5): the user picks the pane, then names it (suggested from the
// pane's directory). controller.Adopt runs in a tea.Cmd; the new task's
// session is unknown / confidence none, so its row lands in UNKNOWN,
// selected. ui.action{adopt} is recorded on success.
type adoptView struct {
	id      int64
	sh      *shared
	all     bool // every unregistered pane, not only agent commands
	loading bool
	loaded  bool
	cands   controller.AdoptCandidates
	listErr string
	sel     int
	height  int

	naming  bool // the name step for cands.Panes[sel]
	name    lineInput
	busy    bool
	nameErr string // Adopt's refusal
}

type (
	adoptListedMsg struct {
		id    int64
		all   bool
		cands controller.AdoptCandidates
		err   error
	}
	adoptDoneMsg struct {
		id  int64
		res controller.AdoptResult
		err error
	}
)

// adoptHeaderRows: title, socket line, blank.
const adoptHeaderRows = 3

func newAdoptView(sh *shared) (view, tea.Cmd) {
	v := adoptView{id: nextViewID(), sh: sh, height: guessBodyRows}
	v, load := v.list(false)
	return v, tea.Batch(load, tea.RequestWindowSize)
}

// list (re)loads the candidates.
func (v adoptView) list(all bool) (adoptView, tea.Cmd) {
	v.all, v.loading, v.listErr = all, true, ""
	sh, id := v.sh, v.id
	return v, func() tea.Msg {
		c, err := sh.ctl.ListAdoptCandidates(sh.ctx, controller.CandidateQuery{All: all})
		return adoptListedMsg{id: id, all: all, cands: c, err: err}
	}
}

func (v adoptView) Update(msg tea.Msg) (view, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.height = msg.Height - viewChrome
	case adoptListedMsg:
		if msg.id != v.id || msg.all != v.all {
			return v, nil
		}
		v.loading, v.loaded, v.cands, v.listErr = false, true, msg.cands, ""
		if msg.err != nil {
			v.listErr = controller.OneLine(msg.err.Error())
		}
		v.sel = min(v.sel, max(len(v.cands.Panes)-1, 0))
	case adoptDoneMsg:
		if msg.id != v.id {
			return v, nil
		}
		v.busy = false
		if msg.err != nil {
			v.nameErr = controller.OneLine(msg.err.Error())
			return v, nil
		}
		id, p := msg.res.TaskID, v.cands.Panes[v.sel]
		return nil, tea.Batch(v.sh.actionCmd(controller.UIActionAdopt, id), selectTask(id),
			flashInfo("adopted "+p.PaneID+" as "+id+" · state unknown until its hooks are set up (agentctl hooks-snippet)"))
	case tea.PasteMsg:
		if v.naming && !v.busy {
			v.name, _ = v.name.Update(msg)
			v.nameErr = ""
		}
	case tea.KeyPressMsg:
		if v.busy {
			return v, nil
		}
		if v.naming {
			return v.nameKey(msg)
		}
		return v.listKey(msg)
	}
	return v, nil
}

func (v adoptView) listKey(k tea.KeyPressMsg) (view, tea.Cmd) {
	keys := v.sh.keys
	n := len(v.cands.Panes)
	switch {
	case key.Matches(k, keys.Back, keys.Quit):
		return nil, nil
	case key.Matches(k, keys.Adopt):
		v.sel = 0
		return v.list(!v.all)
	case key.Matches(k, keys.Up):
		v.sel = max(v.sel-1, 0)
	case key.Matches(k, keys.Down):
		v.sel = max(min(v.sel+1, n-1), 0)
	case key.Matches(k, keys.Open):
		if v.loading || n == 0 {
			return v, nil
		}
		v.naming, v.nameErr = true, ""
		v.name = lineInput{limit: domain.MaxSlugLen}.SetValue(suggestName(v.cands.Panes[v.sel].Cwd))
	}
	return v, nil
}

func (v adoptView) nameKey(k tea.KeyPressMsg) (view, tea.Cmd) {
	keys := v.sh.keys
	s := k.String()
	switch {
	case s == "esc" || (k.Text == "" && key.Matches(k, keys.Back)):
		v.naming, v.nameErr = false, ""
		return v, nil
	case s == "enter" || (k.Text == "" && key.Matches(k, keys.Open)):
		if slugProblem(v.name.Value()) != "" {
			return v, nil
		}
		v.busy, v.nameErr = true, ""
		sh, id := v.sh, v.id
		req := controller.AdoptRequest{PaneID: v.cands.Panes[v.sel].PaneID, Name: v.name.Value()}
		return v, func() tea.Msg {
			res, err := sh.ctl.Adopt(sh.ctx, req)
			return adoptDoneMsg{id: id, res: res, err: err}
		}
	}
	var handled bool
	if v.name, handled = v.name.Update(k); handled {
		v.nameErr = ""
	}
	return v, nil
}

func (v adoptView) Body(f frame) string {
	if v.naming {
		return v.nameBody(f)
	}
	st := f.st
	what := strings.Join(v.cands.Commands, "/") + " panes"
	title := "adopt a running " + strings.Join(v.cands.Commands, "/") + " pane"
	if v.all || len(v.cands.Commands) == 0 {
		what, title = "every unregistered pane", "adopt a running pane"
	}
	lines := []string{st.scope.Render(truncate(title, f.width))}
	switch {
	case v.loading && !v.loaded:
		return strings.Join(append(lines, st.muted.Render("loading…")), "\n")
	case v.listErr != "":
		return strings.Join(append(lines, errorLines(f, v.listErr)...), "\n")
	}
	socket := controller.DisplayPath(v.cands.Socket)
	if v.loading {
		lines = append(lines, st.muted.Render("loading…"))
	} else {
		lines = append(lines, st.muted.Render(truncate(what+" on "+socket, f.width)))
	}
	lines = append(lines, "")
	if len(v.cands.Panes) == 0 {
		none := "No unregistered " + what + " on " + socket
		if !v.all && len(v.cands.Commands) > 0 {
			none = "No unregistered " + strings.Join(v.cands.Commands, "/") + " panes on " + socket
		}
		lines = append(lines, st.text.Render(truncate(none, f.width)))
		if !v.all {
			lines = append(lines, st.muted.Render(truncate(v.sh.keys.Adopt.Help().Key+" lists every pane", f.width)))
		}
		return strings.Join(lines, "\n")
	}
	vis := max(f.height-adoptHeaderRows, 1)
	if len(v.cands.Panes) > vis {
		vis = max(vis-1, 1)
	}
	start := min(max(v.sel-vis+1, 0), max(len(v.cands.Panes)-vis, 0))
	end := min(start+vis, len(v.cands.Panes))
	cols := candidateColumns(v.cands.Panes)
	for i := start; i < end; i++ {
		lines = append(lines, candidateRow(f, cols, v.cands.Panes[i], i == v.sel))
	}
	if more := moreLine(start, len(v.cands.Panes)-end); more != "" {
		lines = append(lines, st.muted.Render(more))
	}
	return strings.Join(lines, "\n")
}

func (v adoptView) nameBody(f frame) string {
	st := f.st
	p := v.cands.Panes[v.sel]
	lines := []string{
		st.scope.Render("adopt "+p.PaneID) + st.muted.Render(truncate(" · "+paneWhere(p)+" · "+safeText(p.Command)+" · "+safeText(p.Cwd), f.width-len("adopt "+p.PaneID))),
		st.muted.Render(truncate("its state stays unknown until its hooks are set up (agentctl hooks-snippet)", f.width)),
		"",
		formField(f, "name", v.name, !v.busy),
	}
	name := v.name.Value()
	switch {
	case name == "":
		lines = append(lines, formHint(f, "1-40 lowercase letters, digits or '-'", st.muted))
	case slugProblem(name) != "":
		lines = append(lines, slugHint(f, name))
	default:
		lines = append(lines, formHint(f, "registers the pane as task "+name, st.muted))
	}
	lines = append(lines, "")
	switch {
	case v.busy:
		lines = append(lines, st.muted.Render(truncate("adopting "+p.PaneID+" as "+name+"…", f.width)))
	case v.nameErr != "":
		lines = append(lines, errorLines(f, v.nameErr)...)
	}
	return strings.Join(lines, "\n")
}

func (v adoptView) Hints() []key.Binding {
	k := v.sh.keys
	switch {
	case v.busy:
		return []key.Binding{k.ForceQuit}
	case v.naming:
		return []key.Binding{help(k.Open, "adopt"), help(k.Back, "back")}
	}
	toggle := help(k.Adopt, "every pane")
	if v.all {
		toggle = help(k.Adopt, "agent panes only")
	}
	return []key.Binding{help(k.Open, "choose"), pairHint(k.Up, k.Down, "move"), toggle, help(k.Back, "back")}
}

// candidate columns: pane id, session:window, command (the cwd takes the rest).
type candCols struct{ pane, where, cmd int }

func candidateColumns(ps []controller.AdoptCandidate) candCols {
	var c candCols
	for _, p := range ps {
		c.pane = max(c.pane, len(p.PaneID))
		c.where = min(max(c.where, len([]rune(paneWhere(p)))), maxNameWidth)
		c.cmd = min(max(c.cmd, len([]rune(safeText(p.Command)))), maxRepoWidth)
	}
	return c
}

func candidateRow(f frame, c candCols, p controller.AdoptCandidate, selected bool) string {
	st := f.st
	marker, style := "  ", st.name
	if selected {
		marker, style = st.marker.Render(">")+" ", st.nameSelected
	}
	sp := strings.Repeat(" ", gap)
	head := padRight(p.PaneID, c.pane) + sp + padRight(truncate(paneWhere(p), c.where), c.where) + sp +
		padRight(truncate(safeText(p.Command), c.cmd), c.cmd) + sp
	rest := f.width - 2 - c.pane - c.where - c.cmd - 3*gap
	return marker + style.Render(head) + st.muted.Render(truncateLeft(safeText(p.Cwd), rest))
}

// paneWhere is `session:window` as tmux shows it.
func paneWhere(p controller.AdoptCandidate) string {
	return safeText(p.SessionName) + ":" + strconv.Itoa(p.WindowIndex)
}

// safeText is tmux-reported text made safe to print (escapes and control
// characters quoted, never interpreted).
func safeText(s string) string { return controller.DisplayPath(domain.SanitizeTerminalText(s)) }

// suggestName turns a directory's base name into a task-name suggestion
// (lowercase, other characters as '-'), or "" when nothing usable is left.
func suggestName(dir string) string {
	base := filepath.Base(filepath.Clean(dir))
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(base) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		switch {
		case ok:
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0 && (r == '-' || r == '_' || r == '.' || unicode.IsSpace(r) || r > unicode.MaxASCII || unicode.IsPunct(r)):
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > domain.MaxSlugLen {
		s = strings.TrimRight(s[:domain.MaxSlugLen], "-")
	}
	if domain.ValidateSlug(s) != nil {
		return ""
	}
	return s
}
