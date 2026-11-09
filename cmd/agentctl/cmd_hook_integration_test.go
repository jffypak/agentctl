package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// hookIT is the end-to-end rig: an isolated tmux server, a temp repo, the
// built agentctl (so every hook is a real `agentctl hook` process spawned by
// fakeagent from the generated settings), and fakeagent as the agent.
type hookIT struct {
	t     *testing.T
	bin   string
	fake  string
	env   testenv.Env
	srv   *testenv.TmuxServer
	repo  *testenv.Repo
	logs  string // fakeagent logs
	paths paths.Paths
}

const itWorkMS = "1000" // long enough for ls --json polling to see working

func newHookIT(t *testing.T) *hookIT {
	t.Helper()
	bin, fake := testenv.BuildAgentctl(t), testenv.BuildFakeAgent(t)
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	logs := t.TempDir()
	// tmux panes get the server's environment, not the client's, so the
	// FAKEAGENT_* knobs travel in the agent argv prefix.
	agentCmd, _ := json.Marshal([]string{"/usr/bin/env", "FAKEAGENT_WORK_MS=" + itWorkMS,
		"FAKEAGENT_LOG=" + filepath.Join(logs, "input.jsonl"), "FAKEAGENT_HOOK_LOG=" + filepath.Join(logs, "hooks.jsonl"), fake})
	env := srv.Env.With("AGENTCTL_AGENT_CMD=" + string(agentCmd))
	p, err := paths.Resolve(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	it := &hookIT{t: t, bin: bin, fake: fake, env: env, srv: srv, repo: testenv.TempRepo(t, env), logs: logs, paths: p}
	t.Cleanup(it.checkHooksFailedOpen)
	return it
}

// agentctl runs the built binary in the repo.
func (it *hookIT) agentctl(args ...string) (stdout, stderr string, code int) {
	it.t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command(it.bin, args...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = it.env.Strings(), it.repo.Root, &out, &errb
	err := cmd.Run()
	if err != nil && cmd.ProcessState == nil {
		it.t.Fatalf("agentctl %v: %v", args, err)
	}
	return out.String(), errb.String(), cmd.ProcessState.ExitCode()
}

type itTask struct {
	AgentState string `json:"agent_state"`
	Confidence string `json:"confidence"`
	LastDetail string `json:"last_detail"`
	Session    *struct {
		ID         int64  `json:"id"`
		TmuxPaneID string `json:"tmux_pane_id"`
	} `json:"session"`
}

func (it *hookIT) task(id string) itTask {
	it.t.Helper()
	out, errOut, code := it.agentctl("ls", "--json")
	var doc struct {
		Tasks []struct {
			ID string `json:"id"`
			itTask
		} `json:"tasks"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &doc) != nil {
		it.t.Fatalf("ls --json: exit %d %s\n%s", code, errOut, out)
	}
	for _, tk := range doc.Tasks {
		if tk.ID == id {
			return tk.itTask
		}
	}
	it.t.Fatalf("task %s not in ls --json:\n%s", id, out)
	return itTask{}
}

// waitState polls `agentctl ls --json` (which also reconciles) until the
// task's agent_state is want.
func (it *hookIT) waitState(id, want string, timeout time.Duration) itTask {
	it.t.Helper()
	var last itTask
	testenv.WaitFor(it.t, timeout, id+" "+want, func() (bool, string) {
		last = it.task(id)
		return last.AgentState == want, last.AgentState + " / " + last.LastDetail
	})
	return last
}

func (it *hookIT) keys(pane string, literal string, enter bool) {
	it.t.Helper()
	it.srv.Run(it.t, "send-keys", "-t", pane, "-l", literal)
	if enter {
		it.srv.Run(it.t, "send-keys", "-t", pane, "Enter")
	}
}

// timeline renders every event of a session with its offset from the first
// one, for failure messages.
func (it *hookIT) timeline(sessionID int64) string {
	it.t.Helper()
	st, err := store.Open(context.Background(), it.paths.DB, store.Options{})
	if err != nil {
		return err.Error()
	}
	defer st.Close()
	evs, _ := st.Events().Since(context.Background(), 0, 0)
	var b strings.Builder
	var t0 time.Time
	for _, e := range evs {
		if e.SessionID != sessionID {
			continue
		}
		if t0.IsZero() {
			t0 = e.TS
		}
		fmt.Fprintf(&b, "\n  +%5dms %s %s", e.TS.Sub(t0).Milliseconds(), e.Type, e.Payload)
	}
	return b.String()
}

// chain is the session's agent.state_changed history as "from>to".
func (it *hookIT) chain(sessionID int64) []string {
	it.t.Helper()
	st, err := store.Open(context.Background(), it.paths.DB, store.Options{})
	if err != nil {
		it.t.Fatal(err)
	}
	defer st.Close()
	evs, err := st.Events().Since(context.Background(), 0, 0)
	if err != nil {
		it.t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Type != domain.EventAgentStateChanged || e.SessionID != sessionID {
			continue
		}
		var p struct{ From, To string }
		_ = json.Unmarshal(e.Payload, &p)
		out = append(out, p.From+">"+p.To)
	}
	return out
}

func (it *hookIT) eventCount(typ string) int {
	it.t.Helper()
	st, err := store.Open(context.Background(), it.paths.DB, store.Options{})
	if err != nil {
		it.t.Fatal(err)
	}
	defer st.Close()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE type = ?`, typ).Scan(&n); err != nil {
		it.t.Fatal(err)
	}
	return n
}

// checkHooksFailedOpen: every hook fakeagent ran exited 0, wrote nothing on
// stdout, and finished well inside its 5 s timeout; nothing was logged as an
// error.
func (it *hookIT) checkHooksFailedOpen() {
	f, err := os.Open(filepath.Join(it.logs, "hooks.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var r struct {
			Event    string `json:"event"`
			Exit     int    `json:"exit"`
			Stdout   string `json:"stdout"`
			Millis   int64  `json:"ms"`
			TimedOut bool   `json:"timed_out"`
		}
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			it.t.Errorf("hook log: %v", err)
			continue
		}
		n++
		if r.Exit != 0 || r.Stdout != "" || r.TimedOut || r.Millis >= hookBudgetLimit.Milliseconds() {
			it.t.Errorf("hook %s: exit %d stdout %q %dms", r.Event, r.Exit, r.Stdout, r.Millis)
		}
	}
	if n == 0 {
		it.t.Error("fakeagent ran no hooks")
	}
	if b, _ := os.ReadFile(filepath.Join(it.paths.Logs, "hook-errors.log")); len(b) != 0 {
		it.t.Errorf("hook-errors.log:\n%s", b)
	}
}

// TestHookStateEndToEnd: new -> starting, ready, working, ready (within 5 s);
// #ask -> awaiting_input; y -> working -> ready; #exit 0 -> stopped. A second
// agent that exits 3 right after its SessionEnd hook reads failed, not stopped.
func TestHookStateEndToEnd(t *testing.T) {
	t.Parallel()
	it := newHookIT(t)
	start := time.Now()
	if out, errOut, code := it.agentctl("new", "t1", "--goal", "first turn"); code != 0 {
		t.Fatalf("new: %d %s %s", code, out, errOut)
	}
	it.waitState("t1", "working", 5*time.Second)
	tk := it.waitState("t1", "ready", 5*time.Second)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("starting -> ready -> working -> ready took %v:%s", d, it.timeline(tk.Session.ID))
	}
	if tk.Confidence != "hook" || tk.Session == nil {
		t.Fatalf("t1 = %+v", tk)
	}
	sess, pane := tk.Session.ID, tk.Session.TmuxPaneID
	want := []string{"starting>ready", "ready>working", "working>ready"}
	if got := it.chain(sess); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("state chain = %v, want %v", got, want)
	}

	it.keys(pane, "#ask", true)
	if tk := it.waitState("t1", "awaiting_input", 5*time.Second); !strings.HasPrefix(tk.LastDetail, "permission") {
		t.Fatalf("awaiting detail = %q", tk.LastDetail)
	}
	it.keys(pane, "y", false) // raw mode: one key, no Enter
	it.waitState("t1", "working", 5*time.Second)
	it.waitState("t1", "ready", 5*time.Second)

	it.keys(pane, "#exit 0", true)
	if tk := it.waitState("t1", "stopped", 5*time.Second); tk.LastDetail != "exit 0" {
		t.Fatalf("stopped detail = %q", tk.LastDetail)
	}
	got := strings.Join(it.chain(sess), " ")
	if !strings.HasSuffix(got, "ready>working working>stopped") || !strings.Contains(got, "working>awaiting_input awaiting_input>working") {
		t.Fatalf("state chain = %s", got)
	}

	// director note: SessionEnd must not mask a non-zero exit
	if _, errOut, code := it.agentctl("new", "t2", "--goal", "#exit 3"); code != 0 {
		t.Fatalf("new t2: %s", errOut)
	}
	if tk := it.waitState("t2", "failed", 5*time.Second); tk.LastDetail != "exit 3" {
		t.Fatalf("t2 = %+v", tk)
	}
	if n := it.eventCount(domain.EventHookOrphan); n != 0 {
		t.Fatalf("%d orphan hooks", n)
	}
}

// TestAdoptedPaneHooksViaSnippet: a pane started by hand with the
// hooks-snippet as its settings (no AGENTCTL_TASK_ID) and then adopted is
// matched by (canonical socket, server pid, pane id) and upgraded to
// confidence hook.
func TestAdoptedPaneHooksViaSnippet(t *testing.T) {
	t.Parallel()
	it := newHookIT(t)
	snippet, _, code := it.agentctl("hooks-snippet")
	if code != 0 {
		t.Fatal("hooks-snippet failed")
	}
	settings := filepath.Join(it.logs, "user-settings.json")
	if err := os.WriteFile(settings, []byte(snippet), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := it.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: "by-hand", Dir: it.repo.Root,
		Argv: []string{"/usr/bin/env", "FAKEAGENT_WORK_MS=" + itWorkMS, "FAKEAGENT_HOOK_LOG=" + filepath.Join(it.logs, "hooks.jsonl"),
			it.fake, "--settings", settings},
	})
	if err != nil {
		t.Fatal(err)
	}
	// its SessionStart hook fired before adoption: an unmanaged pane, nothing recorded
	testenv.WaitFor(t, 0, "fakeagent prompt", func() (bool, string) {
		out, _ := it.srv.TryRun("capture-pane", "-p", "-t", created.PaneID)
		return strings.HasSuffix(strings.TrimSpace(out), ">"), out // capture-pane trims the prompt's trailing space
	})
	if out, errOut, code := it.agentctl("adopt", created.PaneID, "--name", "ad1"); code != 0 {
		t.Fatalf("adopt: %d %s %s", code, out, errOut)
	}
	if tk := it.task("ad1"); tk.AgentState != "unknown" || tk.Confidence != "none" {
		t.Fatalf("adopted = %+v", tk)
	}
	it.keys(created.PaneID, "hello", true)
	if tk := it.waitState("ad1", "working", 5*time.Second); tk.Confidence != "hook" {
		t.Fatalf("working = %+v", tk)
	}
	tk := it.waitState("ad1", "ready", 5*time.Second)
	if got := strings.Join(it.chain(tk.Session.ID), " "); got != "unknown>working working>ready" {
		t.Fatalf("state chain = %s", got)
	}
	if n := it.eventCount(domain.EventHookOrphan); n != 0 {
		t.Fatalf("%d orphan hooks", n)
	}
}
