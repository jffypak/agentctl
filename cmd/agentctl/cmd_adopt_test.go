package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// piRun is runCmd with a chosen stdin (confirmation prompts).
func piRun(t *testing.T, env testenv.Env, cwd, stdin string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	sys := &system{
		stdin: strings.NewReader(stdin), stdout: &out, stderr: &errb,
		environ: env.Strings(), cwd: cwd, now: time.Now, executable: "/opt/agentctl/bin/agentctl",
	}
	return result{run(context.Background(), args, sys), out.String(), errb.String()}
}

// piUserPane starts a session "by hand" on the fixture's isolated server.
func piUserPane(t *testing.T, f *cliFixture, name, dir string, argv ...string) tmux.Created {
	t.Helper()
	c, err := f.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{Name: name, Dir: dir, Argv: argv})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func piEventTypes(t *testing.T, env testenv.Env, task string) []string {
	t.Helper()
	p, err := paths.Resolve(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	evs, err := st.Events().ForTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func lsTasks(t *testing.T, env testenv.Env, cwd string, args ...string) map[string]map[string]any {
	t.Helper()
	r := runCmd(t, env, cwd, append([]string{"ls", "--json"}, args...)...)
	if r.code != 0 {
		t.Fatalf("ls --json = %+v", r)
	}
	var doc struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, task := range doc.Tasks {
		out[task["id"].(string)] = task
	}
	return out
}

func TestAdoptListThenAdoptByPaneID(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	env := f.env.With("AGENTCTL_ADOPT_COMMANDS=sleep")
	if r := runCmd(t, env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	sub := filepath.Join(f.repo.Root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	work := piUserPane(t, f, "work", sub, "/bin/sleep", "600")

	r := runCmd(t, env, f.repo.Root, "adopt")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("adopt (list) = %+v", r)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "Unregistered panes running sleep on tmux server "+f.srv.Socket) ||
		strings.Join(strings.Fields(lines[2]), " ") != "PANE SESSION:WINDOW COMMAND CWD" {
		t.Fatalf("list header:\n%s", r.stdout)
	}
	row := strings.Fields(lines[3])
	if len(row) != 4 || row[0] != work.PaneID || row[1] != "work:0" || row[2] != "sleep" || row[3] != sub {
		t.Fatalf("candidate row %q", lines[3])
	}
	if strings.Count(r.stdout, "%") != 2 || strings.Contains(r.stdout, "jpark-demo") {
		t.Fatalf("registered pane listed or extra rows:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "adopt one with: agentctl adopt "+work.PaneID+" --name <name>") {
		t.Fatalf("hint missing:\n%s", r.stdout)
	}

	r = runCmd(t, env, f.repo.Root, "adopt", work.PaneID, "--name", "foo")
	if r.code != 0 || strings.Count(r.stdout, "\n") != 1 || !strings.HasPrefix(r.stdout, "adopted "+work.PaneID+" as foo · main · "+f.repo.Root+" · state unknown") {
		t.Fatalf("adopt = %+v", r)
	}
	if got := strings.Join(piEventTypes(t, env, "foo"), ","); got != "task.created,session.adopted" {
		t.Fatalf("events = %s", got)
	}
	tasks := lsTasks(t, env, f.repo.Root)
	foo := tasks["foo"]
	sess := foo["session"].(map[string]any)
	if foo["section"] != "initial" || foo["agent_state"] != "unknown" || foo["confidence"] != "none" || sess["origin"] != "adopted" ||
		sess["tmux_pane_id"] != work.PaneID || sess["pane_pid"] != float64(work.PanePID) || sess["tmux_server_pid"] != float64(work.ServerPID) ||
		sess["tmux_socket"] != f.srv.Socket || foo["worktree"] != f.repo.Root || foo["base_sha"] != f.repo.HeadSHA(t) {
		t.Fatalf("ls row = %v", foo)
	}

	r = runCmd(t, env, f.repo.Root, "adopt")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "No unregistered panes running sleep on tmux server ") || !strings.Contains(r.stdout, "adopt --all") {
		t.Fatalf("list after adopt = %+v", r)
	}
}

func TestAdoptErrorsOneLineExit1(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	scratch, _ := filepath.EvalSymlinks(t.TempDir())
	outside := piUserPane(t, f, "scratch", scratch, "/bin/sleep", "600")
	work := piUserPane(t, f, "work", f.repo.Root, "/bin/sleep", "600")
	demoPane := lsTasks(t, f.env, f.repo.Root)["demo"]["session"].(map[string]any)["tmux_pane_id"].(string)

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"pane missing", []string{"adopt", "%999", "--name", "a1"}, "pane %999 not found on tmux server " + f.srv.Socket + " (run `agentctl adopt` to list candidates)"},
		{"already registered", []string{"adopt", demoPane, "--name", "a2"}, `is already registered as task "demo"`},
		{"cwd not in git", []string{"adopt", outside.PaneID, "--name", "a3"}, "working directory " + scratch + " is not inside a git repository"},
		{"name taken", []string{"adopt", work.PaneID, "--name", "demo"}, `task "demo" already exists`},
	}
	for _, c := range cases {
		before := rowCounts(t, f.env)
		r := runCmd(t, f.env, f.repo.Root, c.args...)
		if r.code != 1 || r.stdout != "" || strings.Count(r.stderr, "\n") != 1 || !strings.HasPrefix(r.stderr, "agentctl: ") || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%s: %+v, want exit 1 and one line containing %q", c.name, r, c.want)
		}
		if after := rowCounts(t, f.env); after != before {
			t.Errorf("%s: rows %v -> %v", c.name, before, after)
		}
	}
	for _, args := range [][]string{{"adopt", "%1"}, {"adopt", "--name", "x"}, {"adopt", "%1", "%2", "--name", "x"}, {"adopt", "%1", "--name", "x", "--all"}} {
		if r := runCmd(t, f.env, f.repo.Root, args...); r.code != 2 || !strings.Contains(r.stderr, "usage: agentctl adopt") {
			t.Errorf("%v = %+v, want usage exit 2", args, r)
		}
	}
}
