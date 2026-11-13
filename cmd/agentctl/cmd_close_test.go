package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// piExec runs a real process (a freshly built agentctl, or go build itself).
func piExec(t *testing.T, dir string, env []string, name string, args ...string) result {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", name, err)
		}
		code = ee.ExitCode()
	}
	return result{code, out.String(), errb.String()}
}

func piPane(t *testing.T, f *cliFixture, task string) map[string]any {
	t.Helper()
	return lsTasks(t, f.env, f.repo.Root, "--closed")[task]["session"].(map[string]any)
}

func piAlive(t *testing.T, f *cliFixture, paneID string, panePID float64) bool {
	t.Helper()
	p, ok := f.srv.Snapshot(t).Find(paneID)
	return ok && !p.Dead && float64(p.PanePID) == panePID
}

func TestCloseHidesTaskKeepsSessionWorktreeBranch(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	sess := piPane(t, f, "demo")
	wt := filepath.Join(f.repo.WorktreesDir(), "demo")

	r := runCmd(t, f.env, f.repo.Root, "close", "demo")
	want := "closed demo · tmux session agentctl-demo untouched · worktree kept at " + wt + " · branch jpark/demo kept\n"
	if r.code != 0 || r.stdout != want || r.stderr != "" {
		t.Fatalf("close = %+v\nwant %q", r, want)
	}
	if ls := runCmd(t, f.env, f.repo.Root, "ls"); strings.Contains(ls.stdout, "demo") {
		t.Fatalf("ls still shows demo:\n%s", ls.stdout)
	}
	closed := lsTasks(t, f.env, f.repo.Root, "--closed")["demo"]
	if closed["task_state"] != "closed" || closed["section"] != "closed" {
		t.Fatalf("ls --closed = %v", closed)
	}
	if !piAlive(t, f, sess["tmux_pane_id"].(string), sess["pane_pid"].(float64)) {
		t.Fatal("close killed the session")
	}
	if !strings.Contains(f.repo.Git(t, "worktree", "list", "--porcelain"), "worktree "+wt+"\n") {
		t.Fatal("worktree removed")
	}
	if f.repo.Git(t, "branch", "--list", "jpark/demo") == "" {
		t.Fatal("branch removed")
	}
	if got := piEventTypes(t, f.env, "demo"); got[len(got)-1] != "task.closed" {
		t.Fatalf("events = %v", got)
	}
	r = runCmd(t, f.env, f.repo.Root, "close", "demo")
	if r.code != 1 || strings.Count(r.stderr, "\n") != 1 || !strings.Contains(r.stderr, `task "demo" is already closed`) {
		t.Fatalf("second close = %+v", r)
	}
	if r := runCmd(t, f.env, f.repo.Root, "close", "nope"); r.code != 1 || !strings.Contains(r.stderr, `no task named "nope"`) {
		t.Fatalf("close nope = %+v", r)
	}
	for _, args := range [][]string{{"close"}, {"close", "a", "b"}, {"close", "demo", "--yes"}} {
		if r := runCmd(t, f.env, f.repo.Root, args...); r.code != 2 || !strings.Contains(r.stderr, "usage: agentctl close") {
			t.Errorf("%v = %+v, want usage exit 2", args, r)
		}
	}
}

func TestCloseStopRequiresConfirmationOrYes(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	for _, name := range []string{"demo", "quick"} {
		if r := runCmd(t, f.env, f.repo.Root, "new", name, "--goal", "g"); r.code != 0 {
			t.Fatalf("new = %+v", r)
		}
	}
	demo, quick := piPane(t, f, "demo"), piPane(t, f, "quick")
	for _, answer := range []string{"", "\n", "n\n", "no\n", "yes please\n"} {
		r := piRun(t, f.env, f.repo.Root, answer, "close", "demo", "--stop")
		if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "Stop tmux session agentctl-demo ("+demo["tmux_pane_id"].(string)+") and close demo? The worktree and branch are kept. [y/N] ") ||
			!strings.HasSuffix(r.stderr, "agentctl: close --stop aborted; nothing changed\n") {
			t.Fatalf("answer %q: %+v", answer, r)
		}
		if !piAlive(t, f, demo["tmux_pane_id"].(string), demo["pane_pid"].(float64)) {
			t.Fatalf("answer %q killed the session", answer)
		}
		if lsTasks(t, f.env, f.repo.Root)["demo"] == nil {
			t.Fatalf("answer %q closed the task", answer)
		}
	}
	r := piRun(t, f.env, f.repo.Root, "y\n", "close", "demo", "--stop")
	if r.code != 0 || !strings.HasPrefix(r.stdout, "stopped tmux session agentctl-demo and closed demo · worktree kept at ") {
		t.Fatalf("confirmed = %+v", r)
	}
	if _, ok := f.srv.Snapshot(t).Find(demo["tmux_pane_id"].(string)); ok {
		t.Fatal("confirmed stop left the session running")
	}
	if got := lsTasks(t, f.env, f.repo.Root, "--closed")["demo"]; got["agent_state"] != "stopped" || got["task_state"] != "closed" {
		t.Fatalf("after stop = %v", got)
	}

	r = piRun(t, f.env, f.repo.Root, "", "close", "quick", "--stop", "--yes")
	if r.code != 0 || r.stderr != "" || !strings.HasPrefix(r.stdout, "stopped tmux session agentctl-quick and closed quick") {
		t.Fatalf("--yes = %+v", r)
	}
	if _, ok := f.srv.Snapshot(t).Find(quick["tmux_pane_id"].(string)); ok {
		t.Fatal("--yes left the session running")
	}
	if f.repo.Git(t, "branch", "--list", "jpark/quick") == "" {
		t.Fatal("branch removed by close --stop")
	}
}

func TestCloseStopRefusesAdoptedAndMismatchedSessions(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	ctx := context.Background()
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	user := piUserPane(t, f, "work", f.repo.Root, "/bin/sleep", "600")
	if r := runCmd(t, f.env, f.repo.Root, "adopt", user.PaneID, "--name", "mine"); r.code != 0 {
		t.Fatalf("adopt = %+v", r)
	}
	for _, args := range [][]string{{"close", "mine", "--stop", "--yes"}, {"close", "mine", "--stop"}} {
		r := piRun(t, f.env, f.repo.Root, "y\n", args...)
		if r.code != 1 || strings.Count(r.stderr, "\n") != 1 || !strings.Contains(r.stderr, `refusing to stop "mine": its session was adopted`) {
			t.Fatalf("%v = %+v", args, r)
		}
	}
	if !piAlive(t, f, user.PaneID, float64(user.PanePID)) {
		t.Fatal("adopted pane killed")
	}

	// server restart: demo's $N/%N now belong to an intruder with the same name
	f.srv.Restart(t)
	intruder, err := f.srv.Client().NewSession(ctx, tmux.NewSessionSpec{Name: "agentctl-demo", Dir: f.repo.Root, Argv: []string{"/bin/sleep", "600"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"close", "demo", "--stop", "--yes"}, {"close", "demo", "--stop"}} {
		r := piRun(t, f.env, f.repo.Root, "y\n", args...)
		if r.code != 1 || strings.Count(r.stderr, "\n") != 1 || !strings.Contains(r.stderr, "identity_mismatch") || !strings.Contains(r.stderr, "nothing was killed") {
			t.Fatalf("%v = %+v", args, r)
		}
		if !piAlive(t, f, intruder.PaneID, float64(intruder.PanePID)) {
			t.Fatalf("%v killed the pane that reused the ids", args)
		}
	}
	tasks := lsTasks(t, f.env, f.repo.Root)
	if tasks["demo"]["agent_state"] != "disconnected" || tasks["demo"]["section"] != "needs_you" || tasks["demo"]["last_detail"] != "disconnected: identity_mismatch" {
		t.Fatalf("demo after restart = %v", tasks["demo"])
	}
	if r := runCmd(t, f.env, f.repo.Root, "close", "demo"); r.code != 0 {
		t.Fatalf("plain close of a disconnected task = %+v", r)
	}
	if !piAlive(t, f, intruder.PaneID, float64(intruder.PanePID)) {
		t.Fatal("plain close harmed the intruder")
	}
}

// TestRestartRecoveryFreshProcess is acceptance bullet 3: two tasks, then
// `agentctl ls --json` from a separately built binary in a new process
// reports identical mappings and states, and no tmux session was killed.
func TestRestartRecoveryFreshProcess(t *testing.T) {
	t.Parallel()
	testenv.RequireTool(t, "go")
	f := newCLIFixture(t)
	for _, name := range []string{"one", "two"} {
		if r := runCmd(t, f.env, f.repo.Root, "new", name, "--goal", "g"); r.code != 0 {
			t.Fatalf("new %s = %+v", name, r)
		}
	}
	first := runCmd(t, f.env, f.repo.Root, "ls", "--json")
	snapBefore := f.srv.Snapshot(t)

	bin := filepath.Join(t.TempDir(), "agentctl")
	build := piExec(t, "", nil, "go", "build", "-o", bin, ".")
	if build.code != 0 {
		t.Fatalf("go build: %s", build.stderr)
	}
	second := piExec(t, f.repo.Root, f.env.Strings(), bin, "ls", "--json")
	if second.code != 0 || second.stderr != "" {
		t.Fatalf("fresh ls = %+v", second)
	}
	norm := func(s string) []map[string]any {
		var doc struct {
			Tasks []map[string]any `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(s), &doc); err != nil {
			t.Fatalf("%v\n%s", err, s)
		}
		for _, task := range doc.Tasks {
			delete(task, "age_seconds")
		}
		return doc.Tasks
	}
	a, b := norm(first.stdout), norm(second.stdout)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if len(a) != 2 || string(ja) != string(jb) {
		t.Fatalf("mappings differ across processes:\n%s\n%s", ja, jb)
	}
	snapAfter := f.srv.Snapshot(t)
	if len(snapAfter.Panes) != 2 || snapAfter.ServerPID != snapBefore.ServerPID {
		t.Fatalf("tmux changed: %+v -> %+v", snapBefore, snapAfter)
	}
	for i := range snapBefore.Panes {
		if snapBefore.Panes[i] != snapAfter.Panes[i] || snapAfter.Panes[i].Dead {
			t.Fatalf("pane changed: %+v -> %+v", snapBefore.Panes[i], snapAfter.Panes[i])
		}
	}
	for _, task := range b {
		if task["agent_state"] != "starting" || task["session"].(map[string]any)["tmux_server_pid"] != float64(snapAfter.ServerPID) {
			t.Fatalf("recovered task = %v", task)
		}
	}
}
