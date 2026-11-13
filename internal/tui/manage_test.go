package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// numbered returns "out-01\nout-02\n..." (n lines).
func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "out-%02d\n", i)
	}
	return b.String()
}

// hasLine reports whether some screen line, trimmed, is exactly s.
func hasLine(m Model, s string) bool {
	for _, l := range screen(m) {
		if strings.TrimSpace(l) == s {
			return true
		}
	}
	return false
}

// rowHas reports whether the screen line containing name also contains every part.
func rowHas(m Model, name string, parts ...string) bool {
	row := plain(lineWith(m, name))
	for _, p := range parts {
		if !strings.Contains(row, p) {
			return false
		}
	}
	return row != ""
}

// rowsOf is sectionsOf without the flash line (which sits above the rule).
func rowsOf(m Model) map[string][]string {
	m.flash = flashMsg{}
	_, rows := sectionsOf(m)
	return rows
}

// pagerEnv makes the pager write what it is given to a file the test reads.
func pagerEnv(t *testing.T) (testenv.Env, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "paged.txt")
	return testenv.NewEnv(t).With(fakeAgentCmd, "AGENTCTL_PAGER=cat > '"+out+"'"), out
}

// --- detail: diff summary + last output lines, loaded asynchronously -------

func TestDetailInspectLoadsAsync(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	res := f.newTask(t, "auth-tests", "Write the auth tests")
	changeWorktree(t, f, res.Worktree)
	f.tm.setOutput(res.Identity.PaneID, numbered(15)+"\n\n")

	m := f.managed(t, 120, 40, Options{})
	m, cmd := pressCmd(t, m, "enter")
	text := screenText(m)
	if !strings.Contains(text, "diff       loading…") || !strings.Contains(text, "output     loading…") {
		t.Fatalf("detail before the inspect result:\n%s", text)
	}
	m = runLong(t, m, cmd)
	text = screenText(m)
	for _, want := range []string{
		"diff       2 files +4 -1 · 1 untracked · 1 commit",
		"output     alive",
		"out-06", "out-15",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if strings.Contains(text, "out-05") {
		t.Error("detail shows more than the last 10 lines")
	}
	if t.Failed() {
		t.Fatal("\n" + text)
	}
	// 80x24 has room for fewer lines: the newest are the ones kept
	small := runLong(t, f.managed(t, 80, 24, Options{}), nil)
	small, cmd = pressCmd(t, small, "enter")
	small = runLong(t, small, cmd)
	if txt := screenText(small); !strings.Contains(txt, "out-15") || strings.Contains(txt, "out-06") {
		t.Fatalf("80x24 detail does not keep the newest lines:\n%s", txt)
	}

	// a reused %N: the diff still shows, the output is never captured
	f.tm.setPID(res.Identity.PaneID, 31337)
	f.tm.setOutput(res.Identity.PaneID, "SECRET FROM ANOTHER PROCESS\n")
	m, cmd = pressCmd(t, f.managed(t, 120, 40, Options{}), "enter")
	m = runLong(t, m, cmd)
	text = screenText(m)
	if !strings.Contains(text, "output     pane unavailable (disconnected: identity_mismatch)") ||
		!strings.Contains(text, "2 files +4 -1") || strings.Contains(text, "SECRET") {
		t.Fatalf("reused pane detail:\n%s", text)
	}
}

func TestDetailInspectFollowsStateChanges(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	res := f.newTask(t, "auth-tests", "goal")
	f.tm.setOutput(res.Identity.PaneID, "before\n")
	m, cmd := pressCmd(t, f.managed(t, 120, 40, Options{}), "enter")
	m = runLong(t, m, cmd)
	if !strings.Contains(screenText(m), "before") {
		t.Fatalf("first load:\n%s", screenText(m))
	}
	f.tm.setOutput(res.Identity.PaneID, "after the turn\n")
	if _, err := f.st.Sessions().Transition(context.Background(), res.SessionID, domain.StateReady, domain.ConfidenceHook, "turn finished", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	m = runLong(t, m, pollCmd(m.sh, m.lastID, false))
	if !strings.Contains(screenText(m), "after the turn") {
		t.Fatalf("detail did not re-inspect after the state change:\n%s", screenText(m))
	}
}

// --- d: file list, pager, same view, disconnected ------------------------

func TestDiffViewListsFilesAndPagesTheFullDiff(t *testing.T) {
	t.Parallel()
	env, paged := pagerEnv(t)
	f := newManageFixtureEnv(t, env)
	res := f.newTask(t, "auth-tests", "goal")
	changeWorktree(t, f, res.Worktree)

	m := f.managed(t, 120, 40, Options{})
	m, cmd := pressCmd(t, m, "d")
	if topView(m) == nil || !strings.Contains(screenText(m), "loading…") {
		t.Fatalf("d did not open the diff view:\n%s", screenText(m))
	}
	m = runLong(t, m, cmd)
	text := screenText(m)
	for _, want := range []string{
		"diff  auth-tests",
		"vs main @ " + res.BaseSHA[:8],
		"2 files +4 -1 · 1 untracked · 1 commit",
		"↵ full diff",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("diff view lacks %q", want)
		}
	}
	if !rowHas(m, "README.md", "+1", "-1") || !rowHas(m, "internal/auth/login.go", "+3", "-0") || !rowHas(m, "notes.txt", "+2", "untracked") {
		t.Error("file rows lack their counts")
	}
	if t.Failed() {
		t.Fatal("\n" + text)
	}
	if got := uiActions(t, f.ctl); !slices.Equal(got, []string{"diff:auth-tests"}) {
		t.Fatalf("ui.action = %v", got)
	}

	// enter: the full diff goes to the pager through tea.ExecProcess
	m, cmd = pressCmd(t, m, "enter")
	msgs := collectLong(t, cmd)
	if len(msgs) != 1 {
		t.Fatalf("enter produced %v", msgs)
	}
	pm, ok := msgs[0].(diffPagerMsg)
	if !ok || pm.err != nil || pm.cmd == nil {
		t.Fatalf("pager message = %#v", msgs[0])
	}
	if err := pm.cmd.Run(); err != nil { // what ExecProcess would run on the terminal
		t.Fatal(err)
	}
	out, err := os.ReadFile(paged)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"internal/auth/login.go", "func Login() {}", "notes.txt", "two"} {
		if !strings.Contains(plain(string(out)), want) {
			t.Errorf("paged diff lacks %q:\n%s", want, out)
		}
	}
	next, execCmd := m.Update(pm)
	m = next.(Model)
	if execCmd == nil {
		t.Fatal("the pager command was not handed to tea.ExecProcess")
	}
	// the pager exits: the same view, reloaded, and no second ui.action
	m = runLong(t, m, func() tea.Msg { return diffPagerDoneMsg{id: pm.id, taskID: "auth-tests"} })
	if _, isDiff := topView(m).(diffView); !isDiff || !strings.Contains(screenText(m), "internal/auth/login.go") {
		t.Fatalf("after the pager:\n%s", screenText(m))
	}
	if got := uiActions(t, f.ctl); len(got) != 1 {
		t.Fatalf("ui.action after the pager = %v", got)
	}

	// esc returns to where d was pressed: the list here, the detail there
	if l := press(t, m, "esc"); topView(l) != nil || l.mode != modeList {
		t.Fatal("esc did not return to the list")
	}
	d := pressRun(t, f.managed(t, 120, 40, Options{}), "enter", "d")
	if d = press(t, d, "q"); topView(d) != nil || d.mode != modeDetail {
		t.Fatal("q in the diff view did not return to the detail")
	}
}

func TestDiffViewWorksForDisconnectedTasksAndShowsErrors(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	res := f.newTask(t, "gone", "goal")
	changeWorktree(t, f, res.Worktree)
	f.tm.setPID(res.Identity.PaneID, 31337) // the pane id now belongs to someone else
	if err := f.ctl.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := f.managed(t, 120, 40, Options{})
	if row := plain(lineWith(m, "gone")); !strings.Contains(row, "disconnected") {
		t.Fatalf("not disconnected: %q", row)
	}
	m = pressRun(t, m, "d")
	if !strings.Contains(screenText(m), "internal/auth/login.go") {
		t.Fatalf("disconnected task has no diff:\n%s", screenText(m))
	}

	// a task whose worktree is missing: one-line error, no pager, nothing recorded
	f.seed(t, everyState[1:2]) // flaky-ci, worktree /src/repo-worktrees/flaky-ci
	m = f.managed(t, 120, 40, Options{})
	m.selected = "flaky-ci"
	m = pressRun(t, m, "d")
	if !strings.Contains(screenText(m), "! worktree /src/repo-worktrees/flaky-ci of task flaky-ci is missing") {
		t.Fatalf("missing worktree:\n%s", screenText(m))
	}
	if _, cmd := pressCmd(t, m, "enter"); cmd != nil {
		t.Fatal("enter pages a diff that could not be read")
	}
	if got := uiActions(t, f.ctl); !slices.Equal(got, []string{"diff:gone"}) {
		t.Fatalf("ui.action = %v", got)
	}
}

// --- l: recent capture, scrolling, full capture, pane unavailable ----------

func TestLogsViewScrollsAndPagesTheFullCapture(t *testing.T) {
	t.Parallel()
	env, paged := pagerEnv(t)
	f := newManageFixtureEnv(t, env)
	res := f.newTask(t, "auth-tests", "goal")
	f.tm.setOutput(res.Identity.PaneID, numbered(100))

	m, cmd := pressCmd(t, f.managed(t, 80, 24, Options{}), "l")
	if topView(m) == nil || !strings.Contains(screenText(m), "loading…") {
		t.Fatalf("l did not open the logs view:\n%s", screenText(m))
	}
	m = runLong(t, m, cmd)
	text := screenText(m)
	if !strings.Contains(text, "logs  auth-tests · alive") || !hasLine(m, "out-100") || hasLine(m, "out-01") {
		t.Fatalf("logs view:\n%s", text)
	}
	if got := uiActions(t, f.ctl); !slices.Equal(got, []string{"logs:auth-tests"}) {
		t.Fatalf("ui.action = %v", got)
	}
	// up / k scroll back, down returns; pgup/home jump
	up := press(t, m, "k", "up")
	if hasLine(up, "out-100") || !hasLine(up, "out-98") {
		t.Fatalf("after scrolling up 2:\n%s", screenText(up))
	}
	if down := press(t, up, "j", "down"); !hasLine(down, "out-100") {
		t.Fatalf("down does not scroll back:\n%s", screenText(down))
	}
	top := update(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
	if !hasLine(top, "out-01") || hasLine(top, "out-100") {
		t.Fatalf("home:\n%s", screenText(top))
	}
	if pg := update(t, top, tea.KeyPressMsg{Code: tea.KeyPgDown}); hasLine(pg, "out-01") {
		t.Fatalf("pgdown did not move:\n%s", screenText(pg))
	}
	if end := update(t, top, tea.KeyPressMsg{Code: tea.KeyEnd}); !hasLine(end, "out-100") {
		t.Fatalf("end:\n%s", screenText(end))
	}

	// f: write the full capture and page it; back to the same view
	m, cmd = pressCmd(t, m, "f")
	msgs := collectLong(t, cmd)
	cm, ok := msgs[0].(captureMsg)
	if len(msgs) != 1 || !ok || cm.err != nil || cm.cmd == nil {
		t.Fatalf("f produced %#v", msgs)
	}
	if !strings.HasPrefix(cm.path, filepath.Join(f.ctl.Paths().Logs, "auth-tests")+string(filepath.Separator)) {
		t.Fatalf("capture path %s", cm.path)
	}
	if err := cm.cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if out, _ := os.ReadFile(paged); string(out) != numbered(100) {
		t.Fatalf("paged capture = %q", out)
	}
	next, execCmd := m.Update(cm)
	m = next.(Model)
	if execCmd == nil {
		t.Fatal("the capture was not handed to tea.ExecProcess")
	}
	m = runLong(t, m, func() tea.Msg { return capturePagerDoneMsg{id: cm.id, taskID: "auth-tests", path: cm.path} })
	if _, isLogs := topView(m).(logsView); !isLogs || !strings.Contains(screenText(m), "full capture saved to ") || !strings.Contains(screenText(m), filepath.Base(cm.path)) {
		t.Fatalf("after the pager:\n%s", screenText(m))
	}
	if l := press(t, m, "esc"); topView(l) != nil {
		t.Fatal("esc does not close the logs view")
	}
}

func TestLogsViewMismatchedPaneIsUnavailable(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	res := f.newTask(t, "auth-tests", "goal")
	f.tm.setPID(res.Identity.PaneID, 31337)
	f.tm.setOutput(res.Identity.PaneID, "SECRET FROM ANOTHER PROCESS\n")
	m := pressRun(t, f.managed(t, 80, 24, Options{}), "l")
	text := screenText(m)
	if !strings.Contains(text, "pane unavailable (disconnected: identity_mismatch)") || strings.Contains(text, "SECRET") {
		t.Fatalf("mismatched pane logs:\n%s", text)
	}
	m = pressRun(t, m, "f")
	if txt := screenText(m); !strings.Contains(txt, "! task auth-tests: pane unavailable") || strings.Contains(txt, "SECRET") {
		t.Fatalf("full capture of a mismatched pane:\n%s", txt)
	}
	entries, _ := os.ReadDir(filepath.Join(f.ctl.Paths().Logs, "auth-tests"))
	if len(entries) != 0 {
		t.Fatalf("a mismatched pane was captured: %v", entries)
	}
	if got := uiActions(t, f.ctl); len(got) != 0 {
		t.Fatalf("ui.action for unavailable logs = %v", got)
	}
}

// --- n: name + goal form ---------------------------------------------------

func TestNewTaskFormValidatesAndCreates(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	m := f.managed(t, 120, 40, Options{})
	m = press(t, m, "n")
	if topView(m) == nil || !strings.Contains(screenText(m), "new task") {
		t.Fatalf("n did not open the form:\n%s", screenText(m))
	}
	// typed q / j / n are text, not keys; the slug is validated as typed
	m = typeText(t, m, "Bad q")
	if txt := screenText(m); !strings.Contains(txt, "name   Bad q") || !strings.Contains(txt, `! "Bad q" contains 'B'`) {
		t.Fatalf("inline validation:\n%s", txt)
	}
	if m2, cmd := pressCmd(t, m, "enter"); cmd != nil || topView(m2) == nil {
		t.Fatal("enter on an invalid name did something")
	}
	m = press(t, m, "ctrl+u")
	m = typeText(t, m, "auth-tests")
	if txt := screenText(m); !strings.Contains(txt, "creates branch jpark/auth-tests") || strings.Contains(txt, "! ") {
		t.Fatalf("valid name:\n%s", txt)
	}
	m = press(t, m, "enter") // to the goal
	m = typeText(t, m, "Write the auth tests")
	f.tm.setDelay(300 * time.Millisecond)
	start := time.Now()
	m, cmd := pressCmd(t, m, "enter")
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("submitting blocked Update for %v", d)
	}
	if !strings.Contains(screenText(m), "creating auth-tests…") {
		t.Fatalf("no busy line:\n%s", screenText(m))
	}
	m = press(t, m, "esc") // ignored while the controller works
	if topView(m) == nil {
		t.Fatal("esc dropped the form while NewTask was running")
	}
	f.tm.setDelay(0)
	m = runLong(t, m, cmd)
	if topView(m) != nil {
		t.Fatalf("form still open:\n%s", screenText(m))
	}
	sel, _ := m.selectedTask()
	if sel.ID != "auth-tests" || !strings.Contains(plain(lineWith(m, "auth-tests")), "> auth-tests") ||
		!strings.Contains(plain(lineWith(m, "auth-tests")), "no events yet") {
		t.Fatalf("new row not selected as starting:\n%s", screenText(m))
	}
	if rows := rowsOf(m); !slices.Equal(rows["INITIALSTATE"], []string{"auth-tests"}) { // no hook yet
		t.Fatalf("sections = %v", rows)
	}
	if got := uiActions(t, f.ctl); !slices.Equal(got, []string{"new:auth-tests"}) {
		t.Fatalf("ui.action = %v", got)
	}
	// esc cancels a fresh form and records nothing
	if c := press(t, m, "n", "x", "esc"); topView(c) != nil {
		t.Fatal("esc did not cancel")
	}
	// tab moves between the fields too
	tabbed := update(t, press(t, m, "n"), tea.KeyPressMsg{Code: tea.KeyTab})
	tabbed = typeText(t, tabbed, "g")
	if !strings.Contains(screenText(tabbed), "goal   g") {
		t.Fatalf("tab did not move to the goal:\n%s", screenText(tabbed))
	}
}

// unwrapped is the body text with wrapped lines joined by spaces (long
// inline errors wrap rather than lose their end).
func unwrapped(m Model) string {
	var parts []string
	for _, l := range screen(m) {
		parts = append(parts, strings.TrimSpace(l))
	}
	return strings.Join(parts, " ")
}

func TestNewTaskErrorsKeepTheForm(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	f.repo.Git(t, "branch", "jpark/dup")
	submit := func(m Model, name, goal string) Model {
		m = typeText(t, press(t, m, "n"), name)
		m = typeText(t, press(t, m, "enter"), goal)
		return pressRun(t, m, "enter")
	}
	check := func(m Model, wantErr, name, goal string) {
		t.Helper()
		txt := screenText(m)
		if topView(m) == nil || !strings.Contains(unwrapped(m), wantErr) || !strings.Contains(txt, "\n ! ") || !strings.Contains(txt, "name   "+name) || !strings.Contains(txt, "goal   "+goal) {
			t.Fatalf("want the form kept with %q:\n%s", wantErr, txt)
		}
	}
	m := submit(f.managed(t, 120, 40, Options{}), "dup", "some goal")
	check(m, "! branch jpark/dup already exists in "+f.repo.Root, "dup", "some goal")
	// the name field has focus again: typing fixes the name, enter retries
	m = typeText(t, press(t, m, "backspace", "backspace", "backspace"), "fresh")
	if !strings.Contains(screenText(m), "name   fresh") {
		t.Fatalf("focus not on the name after a branch error:\n%s", screenText(m))
	}

	m = submit(f.managed(t, 120, 40, Options{Cwd: t.TempDir()}), "elsewhere", "g")
	check(m, "is not inside a git repository (cd into a repo and retry)", "elsewhere", "g")

	nf := newManageFixtureEnv(t, testenv.NewEnv(t).With(`AGENTCTL_AGENT_CMD=["agentctl-test-no-such-agent"]`))
	m = submit(nf.managed(t, 120, 40, Options{}), "x1", "g")
	check(m, `! agent command "agentctl-test-no-such-agent" not found on PATH (install Claude Code or set AGENTCTL_AGENT_CMD)`, "x1", "g")

	for _, fx := range []*manageFixture{f, nf} {
		if got := uiActions(t, fx.ctl); len(got) != 0 {
			t.Fatalf("failed creates recorded %v", got)
		}
		ov, err := fx.ctl.Overview(context.Background(), controller.OverviewQuery{AllRepos: true, IncludeClosed: true})
		if err != nil || len(ov.Rows) != 0 {
			t.Fatalf("a failed create left rows: %v %v", ov.Rows, err)
		}
	}
}

// --- a: adopt picker ---------------------------------------------------------

func TestAdoptPickerLandsTheRowInUnknown(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	f.newTask(t, "launched", "goal") // its pane is registered: never a candidate
	f.tm.add(fakePane{Pane: tmux.Pane{SessionID: "$50", PaneID: "%50", PanePID: 5000, SessionName: "work", WindowIndex: 2,
		CurrentCommand: "claude", CurrentPath: f.repo.Root}})
	f.tm.add(fakePane{Pane: tmux.Pane{SessionID: "$51", PaneID: "%51", PanePID: 5100, SessionName: "shell",
		CurrentCommand: "zsh", CurrentPath: "/tmp"}})

	m, cmd := pressCmd(t, f.managed(t, 120, 40, Options{}), "a")
	if topView(m) == nil || !strings.Contains(screenText(m), "loading…") {
		t.Fatalf("a did not open the picker:\n%s", screenText(m))
	}
	m = runLong(t, m, cmd)
	text := screenText(m)
	if !strings.Contains(text, "> %50") || !strings.Contains(text, "work:2") || strings.Contains(text, "%51") || strings.Contains(text, "%91") {
		t.Fatalf("picker:\n%s", text)
	}
	all := pressRun(t, m, "a")
	if txt := screenText(all); !strings.Contains(txt, "%51") || !strings.Contains(txt, "zsh") || !strings.Contains(txt, "every unregistered pane") {
		t.Fatalf("a in the picker does not list every pane:\n%s", txt)
	}
	if txt := screenText(pressRun(t, all, "a")); strings.Contains(txt, "%51") {
		t.Fatalf("a again does not go back to claude panes:\n%s", txt)
	}

	// choose: the name is suggested from the pane's directory, editable
	m = press(t, m, "enter")
	if txt := screenText(m); !strings.Contains(txt, "adopt %50") || !strings.Contains(txt, "name   repo") {
		t.Fatalf("naming step:\n%s", txt)
	}
	m = pressRun(t, m, "enter") // "repo"
	if topView(m) != nil {
		t.Fatalf("adopt did not finish:\n%s", screenText(m))
	}
	sel, _ := m.selectedTask()
	if rows := rowsOf(m); sel.ID != "repo" || !slices.Equal(rows["INITIALSTATE"], []string{"launched", "repo"}) {
		t.Fatalf("adopted row: selected %s, sections %v\n%s", sel.ID, rows, screenText(m))
	}
	if got := uiActions(t, f.ctl); !slices.Equal(got, []string{"adopt:repo"}) {
		t.Fatalf("ui.action = %v", got)
	}

	// adopting under a taken name: the error is inline and the step stays
	f.tm.add(fakePane{Pane: tmux.Pane{SessionID: "$52", PaneID: "%52", PanePID: 5200, SessionName: "w2",
		CurrentCommand: "claude", CurrentPath: f.repo.Root}})
	m = pressRun(t, f.managed(t, 120, 40, Options{}), "a")
	m = pressRun(t, press(t, m, "enter", "ctrl+u"), "l", "a", "u", "n", "c", "h", "e", "d", "enter")
	if txt := screenText(m); topView(m) == nil || !strings.Contains(txt, `! task "launched" already exists`) || !strings.Contains(txt, "name   launched") {
		t.Fatalf("taken name:\n%s", txt)
	}
	// esc steps back to the list, esc again closes
	m = press(t, m, "esc")
	if !strings.Contains(screenText(m), "> %52") {
		t.Fatalf("esc did not return to the list:\n%s", screenText(m))
	}
	if len(press(t, m, "esc").views) != 0 {
		t.Fatal("esc did not close the picker")
	}
}

func TestAdoptPickerEmptyAndErrors(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	m := pressRun(t, f.managed(t, 80, 24, Options{}), "a")
	if txt := screenText(m); !strings.Contains(txt, "No unregistered claude panes") || !strings.Contains(txt, "a lists every pane") {
		t.Fatalf("empty picker:\n%s", txt)
	}
	if _, cmd := pressCmd(t, m, "enter"); cmd != nil {
		t.Fatal("enter with nothing to choose did something")
	}
	// tmux missing: one line, no crash
	env := testenv.NewEnv(t).With("PATH="+testenv.BinDir(t, "git"), fakeAgentCmd)
	repo := testenv.TempRepo(t, env)
	ctl := openController(t, env, nil)
	nm := New(context.Background(), Options{Controller: ctl, Cwd: repo.Root})
	nm = update(t, nm, tea.WindowSizeMsg{Width: 80, Height: 24})
	nm = runLong(t, nm, loadCmd(nm.sh))
	nm = pressRun(t, nm, "a")
	if !strings.Contains(screenText(nm), "! tmux not found on PATH") {
		t.Fatalf("no tmux:\n%s", screenText(nm))
	}
}

// --- x: close confirmation ---------------------------------------------------

func TestCloseConfirmDefaultsToNo(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	res := f.newTask(t, "auth-tests", "goal")
	m := f.managed(t, 120, 40, Options{})
	open := func() Model {
		m2, cmd := pressCmd(t, m, "x")
		if !strings.Contains(screenText(m2), "close auth-tests? y/N") {
			t.Fatalf("x:\n%s", screenText(m2))
		}
		return runLong(t, m2, cmd)
	}
	for _, k := range []string{"n", "enter", "esc", "space", "q", "Y"} {
		c, cmd := pressCmd(t, open(), k)
		if topView(c) != nil || cmd != nil {
			t.Fatalf("%s did not just cancel", k)
		}
	}
	ov, _ := f.ctl.Overview(context.Background(), controller.OverviewQuery{AllRepos: true})
	if len(ov.Rows) != 1 || len(uiActions(t, f.ctl)) != 0 || !f.tm.has(res.Identity.PaneID) {
		t.Fatal("cancelling changed something")
	}

	// y closes; the agent keeps running; ui.action{close}
	c := open()
	if txt := screenText(c); !strings.Contains(txt, "x close and stop…") || !strings.Contains(txt, "the agent keeps running in tmux session agentctl-auth-tests") {
		t.Fatalf("confirm:\n%s", txt)
	}
	c = pressRun(t, c, "y")
	if topView(c) != nil || c.mode != modeList || !strings.Contains(screenText(c), "closed auth-tests") {
		t.Fatalf("after y:\n%s", screenText(c))
	}
	ov, _ = f.ctl.Overview(context.Background(), controller.OverviewQuery{AllRepos: true, IncludeClosed: true})
	if ov.Rows[0].TaskState != domain.TaskClosed || ov.Rows[0].AgentState != domain.StateStarting || !f.tm.has(res.Identity.PaneID) {
		t.Fatalf("close: %+v (pane alive %v)", ov.Rows[0], f.tm.has(res.Identity.PaneID))
	}
	if got := uiActions(t, f.ctl); !slices.Equal(got, []string{"close:auth-tests"}) {
		t.Fatalf("ui.action = %v", got)
	}
}

func TestCloseAndStopNeedsASecondConfirmation(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	res := f.newTask(t, "auth-tests", "goal")
	m := pressRun(t, f.managed(t, 120, 40, Options{}), "x")
	m = press(t, m, "x")
	if txt := screenText(m); !strings.Contains(txt, "stop and close auth-tests? y/N") ||
		!strings.Contains(txt, "kills tmux session agentctl-auth-tests") {
		t.Fatalf("second confirmation:\n%s", txt)
	}
	for _, k := range []string{"enter", "x", "esc", "n"} {
		if c, cmd := pressCmd(t, m, k); topView(c) != nil || cmd != nil {
			t.Fatalf("%s at the stop confirmation did not just cancel", k)
		}
	}
	if !f.tm.has(res.Identity.PaneID) {
		t.Fatal("cancelled stop killed the session")
	}
	m = pressRun(t, m, "y")
	if topView(m) != nil || !strings.Contains(screenText(m), "stopped and closed auth-tests") {
		t.Fatalf("after y:\n%s", screenText(m))
	}
	if f.tm.has(res.Identity.PaneID) {
		t.Fatal("close and stop left the session running")
	}
	ov, _ := f.ctl.Overview(context.Background(), controller.OverviewQuery{AllRepos: true, IncludeClosed: true})
	if ov.Rows[0].TaskState != domain.TaskClosed || ov.Rows[0].AgentState != domain.StateStopped {
		t.Fatalf("after close and stop: %+v", ov.Rows[0])
	}
	if got := uiActions(t, f.ctl); !slices.Equal(got, []string{"close:auth-tests"}) {
		t.Fatalf("ui.action = %v", got)
	}
}

func TestCloseAndStopOnlyForVerifiedLaunchedSessions(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	f.seed(t, everyState)
	f.seedPanes(everyState)
	m := f.managed(t, 120, 40, Options{})
	// legacy-pane: adopted; flaky-ci: disconnected (both first in NEEDS YOU)
	for i, id := range []string{"legacy-pane", "flaky-ci"} {
		c := pressRun(t, press(t, m, slices.Repeat([]string{"j"}, i)...), "x")
		txt := screenText(c)
		if !strings.Contains(txt, "close "+id+"? y/N") || strings.Contains(txt, "x close and stop") {
			t.Fatalf("%s offers stop:\n%s", id, txt)
		}
		if id == "flaky-ci" && !strings.Contains(txt, "close and stop unavailable: refusing to stop") {
			t.Fatalf("launched but unverifiable: no reason given:\n%s", txt)
		}
		if c2, cmd := pressCmd(t, c, "x"); topView(c2) != nil || cmd != nil {
			t.Fatalf("%s: x without a stop option did not cancel", id)
		}
	}
	if len(f.tm.panes) != 12 {
		t.Fatalf("panes changed: %d", len(f.tm.panes))
	}
	// from the detail: the close lands back on the list
	d := pressRun(t, m, "j", "j", "j", "enter", "x") // auth-tests
	d = pressRun(t, d, "y")
	rows := rowsOf(d)
	if d.mode != modeList || topView(d) != nil || slices.Contains(rows["NEEDS YOU"], "auth-tests") {
		t.Fatalf("after closing from the detail:\n%s", screenText(d))
	}
}

// --- Update never blocks ----------------------------------------------------

// TestManageKeysNeverBlockUpdate: with every tmux call taking 300ms, no
// manage key holds Update for more than 50ms; each view shows its
// placeholder while the controller works in a tea.Cmd.
func TestManageKeysNeverBlockUpdate(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	f.newTask(t, "auth-tests", "goal")
	f.tm.setDelay(300 * time.Millisecond)
	base := f.managed(t, 120, 40, Options{})
	steps := [][]string{{"enter"}, {"d"}, {"l"}, {"a"}, {"x"}, {"enter", "d"}, {"enter", "l"}, {"enter", "x"}, {"n"}}
	for _, keys := range steps {
		m := base
		for _, k := range keys {
			start := time.Now()
			next, _ := m.Update(keyMsg(k))
			if d := time.Since(start); d > 50*time.Millisecond {
				t.Fatalf("%v: Update took %v", keys, d)
			}
			m = next.(Model)
		}
		if k := keys[len(keys)-1]; k != "n" && k != "x" && !strings.Contains(screenText(m), "loading…") {
			t.Fatalf("%v: no loading placeholder:\n%s", keys, screenText(m))
		}
	}
}

// --- hardening ---------------------------------------------------------------

// TestManageViewsNeverPrintRawEscapes: text from git, tmux, the registry or
// config.yaml is shown sanitized in every manage view (a terminal reset or a
// DCS string would otherwise act on the user's terminal).
func TestManageViewsNeverPrintRawEscapes(t *testing.T) {
	t.Parallel()
	const evil = "x\x1bc\x1bP1$r\x1b\\y"
	f := newFixture(t)
	f.seed(t, everyState)
	base := f.model(t, 120, 40, Options{})
	authTests := []string{"j", "j", "j"}
	screens := map[string]Model{}
	m := press(t, base, append(authTests, "d")...)
	screens["diff error"] = update(t, m, diffLoadedMsg{id: topView(m).(diffView).id, err: fmt.Errorf("worktree /src/%s is missing", evil)})
	m = press(t, base, append(authTests, "d")...)
	screens["diff paths"] = update(t, m, diffLoadedMsg{id: topView(m).(diffView).id, sum: controller.DiffSummary{BaseRef: evil,
		Total: []controller.FileChange{{Path: evil, Added: 1}}, Untracked: []controller.FileChange{{Path: evil}}, Conflicts: []string{evil}}})
	m = press(t, base, append(authTests, "l")...)
	screens["logs"] = update(t, m, logsLoadedMsg{id: topView(m).(logsView).id, in: controller.Inspection{
		Pane: controller.PaneStatus{Available: true, Note: "alive"}, Output: []string{evil, "\t" + evil}}})
	m = press(t, base, append(authTests, "l")...)
	screens["logs error"] = update(t, m, logsLoadedMsg{id: topView(m).(logsView).id, err: fmt.Errorf("%s", evil)})
	m = press(t, base, "a")
	screens["adopt"] = update(t, m, adoptListedMsg{id: topView(m).(adoptView).id, seq: topView(m).(adoptView).listSeq,
		cands: controller.AdoptCandidates{Socket: evil, Commands: []string{"claude"}, Panes: []controller.AdoptCandidate{
			{PaneID: "%3", PanePID: 1, SessionName: evil, Command: evil, Cwd: "/src/" + evil}}}})
	screens["adopt name"] = press(t, screens["adopt"], "enter")
	m = typeText(t, press(t, base, "n"), "ok")
	m = press(t, m, "enter", "enter")
	screens["new error"] = update(t, m, newTaskDoneMsg{id: topView(m).(newTaskView).id, err: fmt.Errorf("%s", evil)})
	m = press(t, base, append(authTests, "x")...)
	screens["confirm"] = update(t, m, closePlanMsg{id: topView(m).(confirmView).id, stopErr: fmt.Errorf("%s", evil)})
	km, warn := ConfigKeyMap(configEnv(t, "keys:\n  \""+strings.ReplaceAll(evil, "\x1b", `\e`)+"\": d\n").Getenv, "")
	screens["config banner"] = f.model(t, 120, 40, Options{Keys: km, Warning: warn})
	for name, s := range screens {
		if out := plain(s.View().Content); strings.ContainsRune(out, '\x1b') {
			t.Errorf("%s: a raw escape reached the screen: %q", name, out)
		}
	}
}

func TestCloseOnAClosedTaskSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := press(t, f.model(t, 80, 24, Options{}), "/", "c", "l", "o", "s", "e", "d", "enter") // archived
	m = pressRun(t, m, "x")
	if topView(m) != nil || !strings.Contains(screenText(m), "archived is already closed") {
		t.Fatalf("x on a closed task:\n%s", screenText(m))
	}
}

// TestAdoptPickerDropsStaleLists: toggling quickly sends several list
// requests; only the newest may fill the picker (the name step must keep the
// pane it was opened for).
func TestAdoptPickerDropsStaleLists(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := press(t, f.model(t, 80, 24, Options{}), "a", "a", "a") // requests 1 (claude), 2 (all), 3 (claude)
	v := topView(m).(adoptView)
	pane := func(id string) controller.AdoptCandidates {
		return controller.AdoptCandidates{Socket: "/s", Commands: []string{"claude"}, Panes: []controller.AdoptCandidate{{PaneID: id, PanePID: 1, Command: "claude", Cwd: "/w"}}}
	}
	m = update(t, m, adoptListedMsg{id: v.id, seq: v.listSeq - 2, cands: pane("%1")}) // stale, same filter
	if strings.Contains(screenText(m), "%1") {
		t.Fatalf("a stale list was shown:\n%s", screenText(m))
	}
	m = update(t, m, adoptListedMsg{id: v.id, seq: v.listSeq, cands: pane("%2")})
	m = press(t, m, "enter")
	m = update(t, m, adoptListedMsg{id: v.id, seq: v.listSeq - 2, cands: pane("%1")}) // late, while naming
	if txt := screenText(m); !strings.Contains(txt, "adopt %2") {
		t.Fatalf("the name step lost its pane:\n%s", txt)
	}
}

// TestAdoptPickerShowsListingWarning: a degraded listing (no process table)
// says so in place of the socket line, so the header height is unchanged.
func TestAdoptPickerShowsListingWarning(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := press(t, f.model(t, 80, 24, Options{}), "a")
	v := topView(m).(adoptView)
	cands := controller.AdoptCandidates{Socket: "/s", Commands: []string{"claude"}, Warning: "cannot read the process table (ps: boom)"}
	m = update(t, m, adoptListedMsg{id: v.id, seq: v.listSeq, cands: cands})
	if txt := screenText(m); !strings.Contains(txt, "cannot read the process table (ps: boom)") {
		t.Fatalf("warning not shown:\n%s", txt)
	}
}

// TestDetailInspectRetriesALostResult: the shell hands results to the view on
// top, so an Inspect answer that arrives while the diff view is open never
// reaches the detail; the section asks again on a later refresh instead of
// showing "loading…" forever.
func TestDetailInspectRetriesALostResult(t *testing.T) {
	t.Parallel()
	f := newManageFixture(t)
	res := f.newTask(t, "auth-tests", "goal")
	f.tm.setOutput(res.Identity.PaneID, "hello from the pane\n")
	// an answer that lands while another view is open still reaches the
	// detail (Model.forward hands non-input messages to it, m2-prompt-queue)
	m, inspect := pressCmd(t, f.managed(t, 120, 40, Options{}), "enter")
	m = press(t, m, "d")
	m = runLong(t, m, inspect) // lands while the diff view is on top
	m = press(t, m, "esc")
	if !strings.Contains(screenText(m), "hello from the pane") {
		t.Fatalf("the result was lost behind the diff view:\n%s", screenText(m))
	}
	// a request whose answer never comes is retried on a later refresh
	m, _ = pressCmd(t, press(t, m, "esc"), "enter") // drop the new Inspect
	if !strings.Contains(screenText(m), "output     loading…") {
		t.Fatalf("expected the lost result:\n%s", screenText(m))
	}
	m = runLong(t, m, pollCmd(m.sh, m.lastID, true)) // a refresh soon after: not yet retried
	if !strings.Contains(screenText(m), "output     loading…") {
		t.Fatalf("retried too early:\n%s", screenText(m))
	}
	m.detail.inspect.sentAt = time.Now().Add(-inspectRetry - time.Second)
	m = runLong(t, m, pollCmd(m.sh, m.lastID, true))
	if !strings.Contains(screenText(m), "hello from the pane") {
		t.Fatalf("no retry after %v:\n%s", inspectRetry, screenText(m))
	}
}
