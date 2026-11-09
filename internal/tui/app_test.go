package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// names returns the task names listed under each section title, in order.
func sectionsOf(m Model) ([]string, map[string][]string) {
	titles := map[string]bool{}
	for _, s := range domain.Sections() {
		titles[s.Title()] = true
	}
	var order []string
	rows := map[string][]string{}
	cur := ""
	for _, l := range screen(m)[2:] {
		t := strings.TrimSpace(l)
		switch {
		case titles[t]:
			cur = t
			order = append(order, t)
		case cur != "" && t != "" && !strings.HasPrefix(t, "─"):
			rows[cur] = append(rows[cur], strings.Fields(strings.TrimPrefix(t, ">"))[0])
		case strings.HasPrefix(t, "─"):
			cur = ""
		}
	}
	return order, rows
}

func TestHeaderAndSectionsInAttentionOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := f.model(t, 120, 40, Options{})
	ov, err := f.ctl.Overview(context.Background(), controller.OverviewQuery{Cwd: f.repo.Root})
	if err != nil {
		t.Fatal(err)
	}
	if got := screen(m)[0]; strings.TrimSpace(got) != ov.Header() || ov.Header() != "agents / repo · 2 running · 6 need you" {
		t.Fatalf("header = %q, Overview.Header() = %q", got, ov.Header())
	}
	order, rows := sectionsOf(m)
	if !slices.Equal(order, []string{"NEEDS YOU", "IDLE", "RUNNING", "UNKNOWN"}) {
		t.Fatalf("sections = %v", order)
	}
	want := map[string][]string{ // oldest state_since first
		"NEEDS YOU": {"legacy-pane", "flaky-ci", "migrate-db", "auth-tests", "api-docs", "silent-agent"},
		"IDLE":      {"old-spike", "docs-site"},
		"RUNNING":   {"payments", "fresh-task"},
		"UNKNOWN":   {"adopted-shell", "reserving"},
	}
	for s, w := range want {
		if !slices.Equal(rows[s], w) {
			t.Errorf("%s rows = %v, want %v", s, rows[s], w)
		}
	}
	if strings.Contains(screenText(m), "archived") {
		t.Fatal("closed task listed without the closed filter")
	}
}

func TestHeaderOutsideARepoSaysAllRepos(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState[:2])
	m := f.model(t, 80, 24, Options{Cwd: t.TempDir()})
	if got := strings.TrimSpace(screen(m)[0]); got != "agents / all repos · 0 running · 2 need you" {
		t.Fatalf("header = %q", got)
	}
	if !strings.Contains(lineWith(m, "auth-tests"), "repo") {
		t.Fatalf("all-repos rows show the repo: %q", plain(lineWith(m, "auth-tests")))
	}
}

func TestClosedFilterShowsClosedTasks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := press(t, f.model(t, 80, 24, Options{}), "/", "c", "l", "o", "s", "e", "d", "enter")
	order, rows := sectionsOf(m)
	if !slices.Equal(order, []string{"CLOSED"}) || !slices.Equal(rows["CLOSED"], []string{"archived"}) {
		t.Fatalf("closed filter: sections %v rows %v\n%s", order, rows, screenText(m))
	}
	m = press(t, m, "esc")
	if order, _ := sectionsOf(m); order[0] != "NEEDS YOU" || strings.Contains(screenText(m), "archived") {
		t.Fatalf("esc did not clear the closed filter:\n%s", screenText(m))
	}
}

func TestMoveKeysAndFilterOnNameGoalBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := f.model(t, 120, 40, Options{})
	sel := func(m Model) string { s, _ := m.selectedTask(); return s.ID }
	if sel(m) != "legacy-pane" {
		t.Fatalf("initial selection = %s", sel(m))
	}
	steps := []struct{ key, want string }{
		{"j", "flaky-ci"}, {"down", "migrate-db"}, {"k", "flaky-ci"}, {"up", "legacy-pane"}, {"up", "legacy-pane"},
	}
	for _, s := range steps {
		m = press(t, m, s.key)
		if sel(m) != s.want {
			t.Fatalf("after %s selection = %s, want %s", s.key, sel(m), s.want)
		}
		if !strings.HasPrefix(strings.TrimSpace(plain(lineWith(m, s.want))), "> "+s.want) {
			t.Fatalf("marker not on %s:\n%s", s.want, screenText(m))
		}
	}
	for i := 0; i < 20; i++ {
		m = press(t, m, "j")
	}
	if sel(m) != "reserving" {
		t.Fatalf("selection past the end = %s", sel(m))
	}

	filters := map[string][]string{
		"pay":           {"payments"},   // name
		"OAuth":         {"auth-tests"}, // goal, case-insensitive
		"agentctl/docs": {"docs-site"},  // branch
		"tests oauth":   {"auth-tests"}, // every term must match
		"zzz":           nil,            // no match
		"docs":          {"api-docs", "docs-site"},
	}
	for q, want := range filters {
		fm := press(t, f.model(t, 120, 40, Options{}), "/")
		for _, r := range q {
			fm = press(t, fm, string(r))
		}
		_, rows := sectionsOf(fm)
		var got []string
		for _, s := range []string{"NEEDS YOU", "IDLE", "RUNNING", "UNKNOWN"} {
			got = append(got, rows[s]...)
		}
		if !slices.Equal(got, want) {
			t.Errorf("filter %q = %v, want %v", q, got, want)
		}
		if want == nil && !strings.Contains(screenText(fm), "No tasks match “"+q+"”") {
			t.Errorf("no-match text missing:\n%s", screenText(fm))
		}
		// j typed while editing is text, not a move
		if q == "pay" {
			fm = press(t, fm, "enter")
			if !strings.Contains(screen(fm)[2], "/ pay") || fm.filter.editing {
				t.Fatalf("applied filter line = %q", screen(fm)[2])
			}
			fm = press(t, fm, "esc")
			if fm.filter.active() || len(fm.rows()) != 12 {
				t.Fatalf("esc did not clear the filter (%d rows)", len(fm.rows()))
			}
		}
	}
}

func TestDetailShowsEveryField(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := press(t, f.model(t, 120, 40, Options{}), "j", "j", "j", "enter") // auth-tests
	text := screenText(m)
	for _, want := range []string{
		"auth-tests  NEEDS YOU · awaiting input",
		"goal       Write integration tests for the OAuth login flow, including the redirect and the expired-token path",
		"last       permission prompt: Bash(npm test -- auth)",
		"state      awaiting input · confidence hook · since 4m",
		"needs you  awaiting input",
		"agent      claude · launched",
		"worktree   /src/repo-worktrees/auth-tests",
		"branch     agentctl/auth-tests",
		"base       main @ 1a2b3c4",
		"tmux       agentctl-auth-tests · $1 %1 · pane pid 4000",
		"socket     /tmp/tmux-501/default",
		"↵ attach",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if t.Failed() {
		t.Log("\n" + text)
	}
	// q and esc go back to the list, they do not quit
	for _, k := range []string{"q", "esc"} {
		d := press(t, f.model(t, 120, 40, Options{}), "enter")
		back, cmd := pressCmd(t, d, k)
		if back.mode != modeList || isQuit(cmd) {
			t.Fatalf("%s in detail: mode %v quit %v", k, back.mode, isQuit(cmd))
		}
	}
}

func TestUnknownIsHonestAndStoppedIsExited(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := f.model(t, 120, 40, Options{})
	for _, id := range []string{"adopted-shell", "reserving", "legacy-pane", "silent-agent"} {
		row := strings.ToLower(plain(lineWith(m, id)))
		for _, bad := range []string{"ready", "idle", "done"} {
			if strings.Contains(row, bad) {
				t.Errorf("%s row says %q: %q", id, bad, row)
			}
		}
	}
	raw := lineWith(m, "adopted-shell")
	if !strings.Contains(raw, "\x1b[2;") && !strings.Contains(raw, "\x1b[2m") {
		t.Errorf("UNKNOWN row is not faint: %q", raw)
	}
	if !strings.Contains(plain(lineWith(m, "old-spike")), "exited · exit 0") || strings.Contains(screenText(m), "stopped") {
		t.Errorf("stopped row: %q", plain(lineWith(m, "old-spike")))
	}
	if !strings.Contains(lineWith(m, "NEEDS YOU"), "38;5;"+accentDark) || !strings.Contains(lineWith(m, "auth-tests"), "38;5;"+accentDark) {
		t.Errorf("NEEDS YOU does not use the accent: %q", lineWith(m, "NEEDS YOU"))
	}
	if row := lineWith(m, "silent-agent"); !strings.Contains(plain(row), "silent since launch · attach to check") || !strings.Contains(row, "38;5;"+accentDark) {
		t.Errorf("launched agent silent since launch is not NEEDS YOU: %q", plain(row))
	}
	if strings.Contains(lineWith(m, "payments"), "38;5;"+accentDark) {
		t.Errorf("RUNNING row uses the accent: %q", lineWith(m, "payments"))
	}
	// the detail of an unknown task says unknown, faint, never ready
	d := press(t, m, "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "enter") // adopted-shell
	if !strings.Contains(screenText(d), "state      unknown · confidence none · since 2d") {
		t.Fatalf("unknown detail:\n%s", screenText(d))
	}
	d = press(t, m, "j", "j", "j", "j", "j", "enter") // silent-agent: launched, never sent a hook
	for _, want := range []string{"silent-agent  NEEDS YOU · silent since launch · attach to check",
		"state      unknown · confidence none · since 50s", "needs you  silent since launch · attach to check"} {
		if !strings.Contains(screenText(d), want) {
			t.Fatalf("silent-since-launch detail lacks %q:\n%s", want, screenText(d))
		}
	}
	d = press(t, m, "j", "j", "j", "j", "j", "j", "enter") // old-spike
	if !strings.Contains(screenText(d), "state      exited · confidence pane") {
		t.Fatalf("stopped detail:\n%s", screenText(d))
	}
}

func TestNoColorKeepsBoldAndFaintOnly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	for _, m := range []Model{f.model(t, 80, 24, Options{NoColor: true}), press(t, f.model(t, 80, 24, Options{NoColor: true}), "enter")} {
		out := m.View().Content
		if strings.Contains(out, "38;5;") || strings.Contains(out, "48;") || strings.Contains(out, "38;2;") {
			t.Fatalf("NO_COLOR output has colors: %q", out)
		}
	}
	m := f.model(t, 80, 24, Options{NoColor: true})
	if !strings.Contains(lineWith(m, "adopted-shell"), "\x1b[2m") || !strings.Contains(lineWith(m, "NEEDS YOU"), "\x1b[1m") {
		t.Fatalf("NO_COLOR lost faint/bold:\n%q\n%q", lineWith(m, "adopted-shell"), lineWith(m, "NEEDS YOU"))
	}
	light := update(t, f.model(t, 80, 24, Options{}), tea.BackgroundColorMsg{Color: whiteBG{}})
	if !strings.Contains(lineWith(light, "NEEDS YOU"), "38;5;"+accentLight) {
		t.Fatalf("light background keeps the dark accent: %q", lineWith(light, "NEEDS YOU"))
	}
}

type whiteBG struct{}

func (whiteBG) RGBA() (r, g, b, a uint32) { return 0xffff, 0xffff, 0xffff, 0xffff }

func TestStubKeysSayComingIn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	cases := map[string]string{ // d l n a x are real views now (m2-tui-manage, manage_test.go)
		"p": "prompt: coming in m2-prompt-queue",
	}
	for k, want := range cases {
		for _, inDetail := range []bool{false, true} {
			m := f.model(t, 80, 24, Options{})
			if inDetail {
				if k == "n" || k == "a" {
					continue
				}
				m = press(t, m, "enter")
			}
			m, cmd := pressCmd(t, m, k)
			m = run(t, m, cmd)
			if !strings.Contains(screenText(m), want) || len(m.views) != 0 {
				t.Errorf("%s (detail=%v): flash missing %q\n%s", k, inDetail, want, screenText(m))
			}
			if m = press(t, m, "j"); strings.Contains(screenText(m), want) && !inDetail {
				t.Errorf("%s: flash not cleared by the next key", k)
			}
		}
	}
	evs, err := f.ctl.EventsSince(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Type == domain.EventUIAction {
			t.Fatalf("a stub recorded %s", e.Payload)
		}
	}
}

func TestQuitKeys(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	for _, k := range []string{"q", "esc", "ctrl+c"} {
		if _, cmd := pressCmd(t, f.model(t, 80, 24, Options{}), k); !isQuit(cmd) {
			t.Errorf("%s in the list does not quit", k)
		}
	}
	d := press(t, f.model(t, 80, 24, Options{}), "enter")
	if _, cmd := pressCmd(t, d, "ctrl+c"); !isQuit(cmd) {
		t.Error("ctrl+c in detail does not quit")
	}
	fl := press(t, f.model(t, 80, 24, Options{}), "/", "q")
	if _, cmd := pressCmd(t, fl, "q"); isQuit(cmd) || fl.filter.input.Value() != "q" {
		t.Errorf("q typed in the filter quit or was lost (%q)", fl.filter.input.Value())
	}
}

func TestAttachFromDetail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)

	// disconnected: one-line refusal, no tmux call at all
	m := press(t, f.model(t, 80, 24, Options{}), "j", "enter") // flaky-ci
	m, cmd := pressCmd(t, m, "enter")
	m = run(t, m, cmd)
	lines := screen(m)
	flash := lines[len(lines)-3]
	if strings.TrimSpace(flash) != "! attach refused: flaky-ci is disconnected (pane_missing)" {
		t.Fatalf("flash = %q\n%s", flash, screenText(m))
	}
	if got := f.rec.commands(); len(got) != 0 {
		t.Fatalf("refused attach ran tmux: %v", got)
	}

	// identity mismatch (pane pid differs): refused after the read-only probe only
	f.rec.panes = []tmux.Pane{{SessionID: "$1", PaneID: "%1", PanePID: 9999, SessionName: "intruder"}}
	m = press(t, f.model(t, 80, 24, Options{}), "j", "j", "j", "enter") // auth-tests
	m, cmd = pressCmd(t, m, "enter")
	m = run(t, m, cmd)
	if !strings.Contains(screenText(m), "! attach refused: auth-tests's pane is gone or was reused (identity_mismatch)") {
		t.Fatalf("mismatch:\n%s", screenText(m))
	}
	if got := f.rec.commands(); !slices.Equal(got, []string{"display-message", "list-panes"}) {
		t.Fatalf("mismatch tmux calls = %v", got)
	}

	// same server: switch-client to the pane, record ui.action{attach}, quit
	f.rec.panes = []tmux.Pane{{SessionID: "$1", PaneID: "%1", PanePID: 4000, SessionName: "agentctl-auth-tests"}}
	inside := openController(t, f.env.With("TMUX=/tmp/tmux-501/default,100,0"), f.rec)
	run1, err := inside.UIOpened(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m = New(context.Background(), Options{Controller: inside, Cwd: f.repo.Root, RunID: run1})
	m = update(t, m, loadCmd(m.sh)())
	m = press(t, m, "j", "j", "j", "enter")
	m, cmd = pressCmd(t, m, "enter")
	msgs := collect(cmd)
	if len(msgs) != 1 {
		t.Fatalf("attach produced %v", msgs)
	}
	if _, quit := m.Update(msgs[0]); !isQuit(quit) {
		t.Fatalf("switched attach does not quit: %+v", msgs[0])
	}
	cmds := f.rec.commands()
	if cmds[len(cmds)-1] != "switch-client" {
		t.Fatalf("tmux calls = %v", cmds)
	}
	stats, err := inside.Stats(context.Background())
	if err != nil || stats.Attaches != 1 || stats.RunsWithAction != 1 {
		t.Fatalf("ui.action{attach} not recorded: %+v %v", stats, err)
	}

	// outside tmux: the attach runs as an exec'd process (not here) and is recorded too
	m = press(t, f.model(t, 80, 24, Options{RunID: run1}), "j", "j", "j", "enter")
	m, cmd = pressCmd(t, m, "enter")
	msgs = collect(cmd)
	if a, ok := msgs[0].(attachMsg); !ok || a.err != nil || a.res.Cmd == nil {
		t.Fatalf("exec attach = %+v", msgs[0])
	}
	if _, execCmd := m.Update(msgs[0]); execCmd == nil || isQuit(execCmd) {
		t.Fatal("exec attach did not hand over to tea.ExecProcess")
	}
	if s, _ := inside.Stats(context.Background()); s.Attaches != 2 {
		t.Fatalf("attaches = %d", s.Attaches)
	}
	// after the user detaches the list reloads; an attach error is one line
	m = run(t, m, func() tea.Msg { return execDoneMsg{err: errors.New("exit status 1\nsecond line")} })
	if !strings.Contains(screenText(m), "! tmux attach: exit status 1 second line") {
		t.Fatalf("exec error:\n%s", screenText(m))
	}
}

func TestEmptyStateShowsTheBinding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	binding := "bind-key a display-popup -E -w 80% -h 75% -d '#{pane_current_path}' '" + testAgentctl + "'"
	for _, w := range []int{80, 120} {
		m := f.model(t, w, 24, Options{Binding: binding})
		text := screenText(m)
		if !strings.Contains(text, emptyText) {
			t.Fatalf("empty state lacks %q:\n%s", emptyText, text)
		}
		var joined []string
		for _, l := range screen(m) {
			if s := strings.TrimSpace(l); strings.HasPrefix(s, "bind-key") || (len(joined) > 0 && s != "" && !strings.HasPrefix(s, "─")) {
				joined = append(joined, s)
				continue
			}
			if len(joined) > 0 {
				break
			}
		}
		if strings.Join(joined, " ") != binding {
			t.Fatalf("width %d: binding shown as %q, want %q", w, strings.Join(joined, " "), binding)
		}
	}
	f.seed(t, everyState[len(everyState)-1:]) // only a closed task
	m := f.model(t, 80, 24, Options{Binding: binding})
	if !strings.Contains(screenText(m), emptyText) || !strings.Contains(screenText(m), "1 closed · /closed lists them") {
		t.Fatalf("empty with closed:\n%s", screenText(m))
	}
}

func TestErrorBanners(t *testing.T) {
	t.Parallel()
	// tmux missing: the preflight banner is one line and the list still works
	env := testenv.NewEnv(t).With("PATH="+testenv.BinDir(t, "git"), `AGENTCTL_AGENT_CMD=["/bin/sh"]`)
	repo := testenv.TempRepo(t, env)
	ctl := openController(t, env, nil)
	m := New(context.Background(), Options{Controller: ctl, Cwd: repo.Root})
	m = update(t, m, loadCmd(m.sh)())
	m = run(t, m, preflightCmd(m.sh))
	if got := screen(m)[2]; strings.TrimSpace(got) != "! tmux not found on PATH (install tmux 3.3 or newer)" {
		t.Fatalf("banner = %q\n%s", got, screenText(m))
	}
	if !strings.Contains(screenText(m), emptyText) {
		t.Fatal("banner replaced the list")
	}

	// registry unopenable: banner only, q quits, nothing else happens
	dead := New(context.Background(), Options{OpenErr: errors.New("cannot open registry /x/agentctl.db:\nunable to open database file")})
	dead = update(t, dead, tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := strings.TrimSpace(screen(dead)[2]); got != "! cannot open registry /x/agentctl.db: unable to open database file" {
		t.Fatalf("banner = %q", got)
	}
	for _, k := range []string{"j", "enter", "/", "p", "n"} {
		next, cmd := pressCmd(t, dead, k)
		if cmd != nil || len(next.views) != 0 || next.mode != modeList {
			t.Fatalf("%s did something without a registry", k)
		}
	}
	if _, cmd := pressCmd(t, dead, "q"); !isQuit(cmd) {
		t.Fatal("q does not quit the error screen")
	}
	if cmd := dead.Init(); isQuit(cmd) {
		t.Fatal("init quits")
	}

	// a registry read error becomes a banner, not a crash; recovery clears it
	f := newFixture(t)
	f.seed(t, everyState)
	m = f.model(t, 80, 24, Options{})
	m = update(t, m, polledMsg{err: errors.New("database is locked")})
	if !strings.Contains(screen(m)[2], "! cannot read the registry: database is locked") || !strings.Contains(screenText(m), "auth-tests") {
		t.Fatalf("load error:\n%s", screenText(m))
	}
	m = update(t, m, loadCmd(m.sh)())
	if strings.Contains(screenText(m), "cannot read") {
		t.Fatal("banner not cleared after a good read")
	}
}

func TestRefreshFollowsTheSelectedTask(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := press(t, f.model(t, 120, 40, Options{}), "j", "j", "j", "j", "j", "j", "j", "j") // payments (RUNNING)
	if s, _ := m.selectedTask(); s.ID != "payments" {
		t.Fatalf("selected %s", s.ID)
	}
	d := press(t, m, "enter")
	sess, err := f.st.Sessions().LatestForTask(context.Background(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Sessions().Transition(context.Background(), sess.ID, domain.StateAwaitingInput, domain.ConfidenceHook, "permission prompt", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	d = run(t, d, pollCmd(d.sh, d.lastID, false))
	if !strings.Contains(screenText(d), "payments  NEEDS YOU · awaiting input") {
		t.Fatalf("detail did not follow the change:\n%s", screenText(d))
	}
	l := press(t, d, "esc")
	if s, _ := l.selectedTask(); s.ID != "payments" || !strings.Contains(plain(lineWith(l, "payments")), "> payments") {
		t.Fatalf("selection lost after the row moved sections:\n%s", screenText(l))
	}
	_, rows := sectionsOf(l)
	if !slices.Contains(rows["NEEDS YOU"], "payments") {
		t.Fatalf("payments not in NEEDS YOU: %v", rows)
	}
}
