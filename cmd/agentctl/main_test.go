package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
)

const sleepAgentJSON = `["sh","-c","exec sleep 600","agent"]`

type result struct {
	code           int
	stdout, stderr string
}

func runCmd(t *testing.T, env testenv.Env, cwd string, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	sys := &system{
		stdin: strings.NewReader(""), stdout: &out, stderr: &errb,
		environ: env.Strings(), cwd: cwd, now: time.Now, executable: "/opt/agentctl/bin/agentctl",
	}
	code := run(context.Background(), args, sys)
	return result{code, out.String(), errb.String()}
}

type cliFixture struct {
	env  testenv.Env
	srv  *testenv.TmuxServer
	repo *testenv.Repo
}

func newCLIFixture(t *testing.T) *cliFixture {
	t.Helper()
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	env := srv.Env.With("AGENTCTL_AGENT_CMD=" + sleepAgentJSON)
	return &cliFixture{env: env, srv: srv, repo: testenv.TempRepo(t, env)}
}

// rowCounts opens the registry read-side to check for partial rows.
func rowCounts(t *testing.T, env testenv.Env) [3]int {
	t.Helper()
	p, err := paths.Resolve(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.DB); err != nil {
		return [3]int{}
	}
	st, err := store.Open(context.Background(), p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var c [3]int
	for i, table := range []string{"tasks", "sessions", "events"} {
		if err := st.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&c[i]); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestStubSubcommandsExit2(t *testing.T) {
	env := testenv.NewEnv(t)
	stubs := map[string]string{
		"tui": "m2-tui-inbox", "adopt": "m1-pane-identity", "show": "m1-inspect", "diff": "m1-inspect",
		"send": "m2-prompt-queue", "close": "m1-pane-identity", "hook": "m1-hook-state", "deliver": "m2-prompt-queue",
		"stats": "m2-tui-inbox", "hooks-snippet": "m1-hook-state", "tmux-binding": "m2-tui-inbox",
	}
	for name, issue := range stubs {
		r := runCmd(t, env, t.TempDir(), name, "whatever")
		want := "agentctl " + name + ": not implemented yet (" + issue + ")\n"
		if r.code != 2 || r.stderr != want || r.stdout != "" {
			t.Errorf("%s: code=%d stdout=%q stderr=%q, want 2 and %q", name, r.code, r.stdout, r.stderr, want)
		}
	}
	if r := runCmd(t, env, t.TempDir()); r.code != 2 || !strings.Contains(r.stderr, "not implemented yet (m2-tui-inbox)") {
		t.Errorf("bare agentctl = %+v, want the tui stub", r)
	}
	if len(commands) != len(stubs)+2 {
		t.Errorf("command table has %d entries, want %d stubs + new + ls", len(commands), len(stubs))
	}
}

func TestHelpAndUnknownCommand(t *testing.T) {
	env := testenv.NewEnv(t)
	r := runCmd(t, env, t.TempDir(), "help")
	if r.code != 0 {
		t.Fatalf("help = %+v", r)
	}
	for _, c := range commands {
		if !strings.Contains(r.stdout, c.name) {
			t.Errorf("help does not list %s", c.name)
		}
	}
	if r := runCmd(t, env, t.TempDir(), "frobnicate"); r.code != 2 || !strings.Contains(r.stderr, "unknown command") {
		t.Fatalf("unknown = %+v", r)
	}
	if r := runCmd(t, env, t.TempDir(), "new"); r.code != 2 || !strings.Contains(r.stderr, "usage: agentctl new") {
		t.Fatalf("new without name = %+v", r)
	}
	if r := runCmd(t, env, t.TempDir(), "ls", "--bogus"); r.code != 2 {
		t.Fatalf("ls --bogus = %+v", r)
	}
}

func TestNewThenLsTextAndJSON(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "write the tests")
	if r.code != 0 || strings.Count(r.stdout, "\n") != 1 || !strings.HasPrefix(r.stdout, "started demo · agentctl/demo · ") || r.stderr != "" {
		t.Fatalf("new = %+v", r)
	}
	// flags before the name work too
	if r := runCmd(t, f.env, f.repo.Root, "new", "--goal", "second", "demo2"); r.code != 0 {
		t.Fatalf("new --goal first = %+v", r)
	}

	r = runCmd(t, f.env, f.repo.Root, "ls")
	if r.code != 0 {
		t.Fatalf("ls = %+v", r)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if lines[0] != "agents / repo · 2 running · 0 need you" {
		t.Fatalf("header = %q", lines[0])
	}
	if got := strings.Fields(lines[2]); strings.Join(got, " ") != "NAME SECTION STATE CONFIDENCE AGE BRANCH WORKTREE" {
		t.Fatalf("columns = %q", lines[2])
	}
	row := strings.Fields(lines[3])
	wt := filepath.Join(f.repo.WorktreesDir(), "demo")
	if len(row) != 7 || row[0] != "demo" || row[1] != "RUNNING" || row[2] != "starting" || row[3] != "none" ||
		!strings.HasSuffix(row[4], "s") || row[5] != "agentctl/demo" || row[6] != wt {
		t.Fatalf("row = %q", lines[3])
	}

	r = runCmd(t, f.env, f.repo.Root, "ls", "--json")
	var doc struct {
		SchemaVersion int `json:"schema_version"`
		Scope         struct {
			AllRepos bool   `json:"all_repos"`
			Repo     string `json:"repo"`
			RepoRoot string `json:"repo_root"`
		} `json:"scope"`
		Counts map[string]int    `json:"counts"`
		Tasks  []json.RawMessage `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || r.code != 0 {
		t.Fatalf("ls --json = %v\n%s", err, r.stdout)
	}
	if doc.SchemaVersion != 1 || doc.Scope.AllRepos || doc.Scope.Repo != "repo" || doc.Scope.RepoRoot != f.repo.Root ||
		doc.Counts["running"] != 2 || len(doc.Tasks) != 2 {
		t.Fatalf("doc = %+v", doc)
	}
	var task map[string]any
	_ = json.Unmarshal(doc.Tasks[0], &task)
	for _, key := range []string{"id", "name", "goal", "repo", "repo_root", "task_state", "section", "label", "agent_state",
		"confidence", "state_since", "age_seconds", "branch", "worktree", "base_ref", "base_sha", "last_detail",
		"created_at", "session", "messages"} {
		if _, ok := task[key]; !ok {
			t.Errorf("task JSON missing %q", key)
		}
	}
	if task["id"] != "demo" || task["agent_state"] != "starting" || task["section"] != "running" || task["worktree"] != wt {
		t.Fatalf("task = %v", task)
	}
	sess := task["session"].(map[string]any)
	for _, key := range []string{"id", "agent_type", "origin", "tmux_socket", "tmux_server_pid", "tmux_session_id",
		"tmux_session_name", "tmux_pane_id", "pane_pid", "agent_session_id", "ended_at"} {
		if _, ok := sess[key]; !ok {
			t.Errorf("session JSON missing %q", key)
		}
	}
	if sess["tmux_socket"] != f.srv.Socket || sess["tmux_session_name"] != "agentctl-demo" {
		t.Fatalf("session = %v", sess)
	}
}

// TestNewErrorsAreOneLineExit1NoPartialRow covers every listed preflight /
// conflict error: one actionable line, exit 1, nothing written.
func TestNewErrorsAreOneLineExit1NoPartialRow(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "first"); r.code != 0 {
		t.Fatalf("seed new = %+v", r)
	}
	f.repo.Git(t, "branch", "agentctl/br")
	if err := os.MkdirAll(filepath.Join(f.repo.WorktreesDir(), "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	notRepo := t.TempDir()
	cases := []struct {
		name string
		env  testenv.Env
		cwd  string
		args []string
		want string
	}{
		{"tmux missing", f.env.With("PATH=" + testenv.BinDir(t, "git", "sh")), f.repo.Root, []string{"new", "a1", "--goal", "g"}, "tmux not found on PATH"},
		{"git missing", f.env.With("PATH=" + testenv.BinDir(t, "tmux", "sh")), f.repo.Root, []string{"new", "a2", "--goal", "g"}, "git not found on PATH"},
		{"not a repo", f.env, notRepo, []string{"new", "a3", "--goal", "g"}, "is not inside a git repository"},
		{"agent cmd not found", f.env.With(`AGENTCTL_AGENT_CMD=["agentctl-no-such-agent"]`), f.repo.Root, []string{"new", "a4", "--goal", "g"}, `agent command "agentctl-no-such-agent" not found on PATH`},
		{"claude default not found", f.env.Without("AGENTCTL_AGENT_CMD").With("PATH=" + testenv.BinDir(t, "git", "tmux")), f.repo.Root, []string{"new", "a5", "--goal", "g"}, `agent command "claude" not found on PATH`},
		{"invalid slug", f.env, f.repo.Root, []string{"new", "Bad.Name", "--goal", "g"}, "invalid task name"},
		{"slug with slash", f.env, f.repo.Root, []string{"new", "a/b", "--goal", "g"}, "invalid task name"},
		{"task exists", f.env, f.repo.Root, []string{"new", "demo", "--goal", "g"}, `task "demo" already exists`},
		{"branch exists", f.env, f.repo.Root, []string{"new", "br", "--goal", "g"}, "branch agentctl/br already exists"},
		{"worktree path exists", f.env, f.repo.Root, []string{"new", "wt", "--goal", "g"}, "worktree path " + filepath.Join(f.repo.WorktreesDir(), "wt") + " already exists"},
		{"goal looks like a flag", f.env, f.repo.Root, []string{"new", "a6", "--goal", "--dangerously-skip-permissions"}, "goal must not start with '-'"},
	}
	for _, c := range cases {
		before := rowCounts(t, f.env)
		r := runCmd(t, c.env, c.cwd, c.args...)
		after := rowCounts(t, f.env)
		if r.code != 1 {
			t.Errorf("%s: exit %d, want 1 (%+v)", c.name, r.code, r)
			continue
		}
		if strings.Count(r.stderr, "\n") != 1 || !strings.HasPrefix(r.stderr, "agentctl: ") || !strings.Contains(r.stderr, c.want) || r.stdout != "" {
			t.Errorf("%s: stderr %q, want one line containing %q", c.name, r.stderr, c.want)
		}
		if before != after {
			t.Errorf("%s: partial rows written: %v -> %v", c.name, before, after)
		}
	}
	if got := strings.Count(f.repo.Git(t, "worktree", "list", "--porcelain"), "worktree "); got != 2 {
		t.Fatalf("worktrees = %d, want main + demo only", got)
	}
}

func TestNewTmuxFailureAfterWorktreeExit1TaskKept(t *testing.T) {
	t.Parallel()
	testenv.RequireTool(t, "tmux")
	bad := "/tmp/agentctl-missing-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "/sock"
	env := testenv.NewEnv(t).With("AGENTCTL_AGENT_CMD="+sleepAgentJSON, "AGENTCTL_TMUX_SOCKET="+bad)
	repo := testenv.TempRepo(t, env)
	r := runCmd(t, env, repo.Root, "new", "demo", "--goal", "g")
	if r.code != 1 || strings.Count(r.stderr, "\n") != 1 || !strings.Contains(r.stderr, "created but its agent failed to start") {
		t.Fatalf("new = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(repo.WorktreesDir(), "demo")); err != nil {
		t.Fatalf("worktree not kept: %v", err)
	}
	r = runCmd(t, env, repo.Root, "ls")
	if !strings.Contains(r.stdout, "NEEDS YOU") || !strings.Contains(r.stdout, "failed") {
		t.Fatalf("ls after launch failure = %q", r.stdout)
	}
}

func TestNewIgnoresCallerTMUXWhenSocketIsExplicit(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	fakeTMUX := filepath.Join(t.TempDir(), "user-default-sock")
	env := f.env.With("TMUX="+fakeTMUX+",1,0", "TMUX_PANE=%0")
	if r := runCmd(t, env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	found := false
	for _, p := range f.srv.Snapshot(t).Panes {
		found = found || p.SessionName == "agentctl-demo"
	}
	if !found {
		t.Fatal("session not on the isolated server")
	}
	if _, err := os.Stat(fakeTMUX); err == nil {
		t.Fatal("the $TMUX socket was touched")
	}
}

func TestLsScopesToRepoOrAllRepos(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	other := testenv.TempRepo(t, f.env)
	for _, c := range []struct{ cwd, name string }{{f.repo.Root, "a1"}, {other.Root, "b1"}} {
		if r := runCmd(t, f.env, c.cwd, "new", c.name, "--goal", "g"); r.code != 0 {
			t.Fatalf("new %s = %+v", c.name, r)
		}
	}
	inA := runCmd(t, f.env, f.repo.Root, "ls")
	if !strings.Contains(inA.stdout, "a1") || strings.Contains(inA.stdout, "b1") {
		t.Fatalf("ls in repo A = %q", inA.stdout)
	}
	outside := runCmd(t, f.env, t.TempDir(), "ls")
	if !strings.HasPrefix(outside.stdout, "agents / all repos · 2 running") || !strings.Contains(outside.stdout, "a1") || !strings.Contains(outside.stdout, "b1") {
		t.Fatalf("ls outside a repo = %q", outside.stdout)
	}
	fresh := testenv.TempRepo(t, f.env)
	empty := runCmd(t, f.env, fresh.Root, "ls")
	if !strings.Contains(empty.stdout, "No agents yet") {
		t.Fatalf("ls in an unused repo = %q", empty.stdout)
	}
}

func TestLsClosedFilter(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "gone", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	p, _ := paths.Resolve(f.env.Getenv)
	st, err := store.Open(context.Background(), p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Closing is m1-pane-identity's (tasks_close.go); simulate its effect.
	if _, err := st.DB().Exec(`UPDATE tasks SET state = 'closed', closed_at = 1 WHERE id = 'gone'`); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if r := runCmd(t, f.env, f.repo.Root, "ls"); strings.Contains(r.stdout, "gone") {
		t.Fatalf("closed task listed: %q", r.stdout)
	}
	r := runCmd(t, f.env, f.repo.Root, "ls", "--closed")
	if !strings.Contains(r.stdout, "gone") || !strings.Contains(r.stdout, "CLOSED") {
		t.Fatalf("ls --closed = %q", r.stdout)
	}
}

func TestFormatAge(t *testing.T) {
	cases := map[time.Duration]string{0: "0s", 59 * time.Second: "59s", 61 * time.Second: "1m", 3 * time.Hour: "3h", 47 * time.Hour: "47h", 72 * time.Hour: "3d"}
	for d, want := range cases {
		if got := formatAge(d); got != want {
			t.Errorf("formatAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestParseInterspersed(t *testing.T) {
	sys := &system{stderr: &bytes.Buffer{}}
	fs := newFlagSet(sys, "x")
	g := fs.String("goal", "", "")
	pos, err := parseInterspersed(fs, []string{"name", "--goal", "g", "--", "--literal"})
	if err != nil || *g != "g" || strings.Join(pos, ",") != "name,--literal" {
		t.Fatalf("pos=%v goal=%q err=%v", pos, *g, err)
	}
}
