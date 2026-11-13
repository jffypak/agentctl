// Package e2e proves the Phase-1 acceptance criteria (agent-control-spec.md
// §11) and the ARCHITECTURE.md invariants end to end: the real agentctl
// binary (TUI on a real terminal, CLI, hooks, deliver) against fakeagent in
// an isolated tmux server, a temp git repo and a temp AGENTCTL_HOME. Every
// wait is on a condition (screen content, registry state, a log line) with a
// generous bound; nothing sleeps. Tests skip when tmux or git is missing.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
)

// stateWait bounds a wait for an agent state. A healthy fakeagent turn takes
// well under a second; the bound only matters for a broken run or a heavily
// loaded machine (-race -count=10 with every test in parallel).
const stateWait = 30 * time.Second

// hookLimit is invariant 7's budget: a hook finishes in under 2 s.
const hookLimit = 2 * time.Second

// rig is one hermetic world: isolated tmux server, temp repo, temp
// AGENTCTL_HOME, the built agentctl (hooks and deliver are real processes it
// spawns) and fakeagent as the agent.
type rig struct {
	t     *testing.T
	bin   string
	fake  string
	srv   *testenv.TmuxServer
	env   testenv.Env // srv.Env: AGENTCTL_HOME, socket, hermetic HOME/XDG/git
	repo  *testenv.Repo
	logs  string
	paths paths.Paths
	knobs []string // FAKEAGENT_* / AGENTCTL_* knobs for every agent
}

func newRig(t *testing.T, knobs ...string) *rig {
	t.Helper()
	testenv.RequireTool(t, "tmux")
	testenv.RequireTool(t, "git")
	bin, fake := testenv.BuildAgentctl(t), testenv.BuildFakeAgent(t)
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	env := srv.Env
	p, err := paths.Resolve(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, bin: bin, fake: fake, srv: srv, env: env, repo: testenv.TempRepo(t, env),
		logs: t.TempDir(), paths: p, knobs: knobs}
	// Cleanups run last-in first-out: these run before the server is killed.
	t.Cleanup(r.waitDeliversDone)
	t.Cleanup(r.checkHooksFailedOpen)
	return r
}

// agentEnv is the environment for launching agent name: tmux panes get the
// server's environment, so the fakeagent knobs and its per-agent logs travel
// in the agent argv prefix (AGENTCTL_AGENT_CMD), and the hooks and deliver
// processes the agent spawns inherit them.
func (r *rig) agentEnv(name string) testenv.Env {
	argv := append([]string{"/usr/bin/env",
		"FAKEAGENT_LOG=" + r.inputLog(name),
		"FAKEAGENT_HOOK_LOG=" + filepath.Join(r.logs, name+".hooks.jsonl"),
		"AGENTCTL_DELIVER_SETTLE=50ms"}, r.knobs...)
	cmd, _ := json.Marshal(append(argv, r.fake))
	return r.env.With("AGENTCTL_AGENT_CMD=" + string(cmd))
}

func (r *rig) inputLog(name string) string { return filepath.Join(r.logs, name+".input.jsonl") }

// agentctl runs the built binary in the repo with env (r.env when nil).
func (r *rig) agentctlEnv(env testenv.Env, args ...string) (stdout, stderr string, code int) {
	r.t.Helper()
	if env == nil {
		env = r.env
	}
	var out, errb bytes.Buffer
	cmd := exec.Command(r.bin, args...)
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = env.Strings(), r.repo.Root, &out, &errb
	if err := cmd.Run(); err != nil && cmd.ProcessState == nil {
		r.t.Fatalf("agentctl %v: %v", args, err)
	}
	return out.String(), errb.String(), cmd.ProcessState.ExitCode()
}

func (r *rig) agentctl(args ...string) (stdout, stderr string, code int) {
	r.t.Helper()
	return r.agentctlEnv(nil, args...)
}

// newAgent runs `agentctl new name [--goal goal]` with name's agent env.
func (r *rig) newAgent(name, goal string) {
	r.t.Helper()
	args := []string{"new", name}
	if goal != "" {
		args = append(args, "--goal", goal)
	}
	if out, errOut, code := r.agentctlEnv(r.agentEnv(name), args...); code != 0 {
		r.t.Fatalf("agentctl new %s: exit %d\n%s%s", name, code, out, errOut)
	}
}

// startTUI runs the real agentctl TUI in session (120x40) with env (r.env
// when nil), and waits for its header.
func (r *rig) startTUI(session string, env testenv.Env) *testenv.TUI {
	r.t.Helper()
	if env == nil {
		env = r.env
	}
	u := r.srv.StartTUI(r.t, r.bin, env, testenv.TUIOptions{Session: session, Cwd: r.repo.Root})
	u.WaitText(r.t, "agents / repo")
	return u
}

// lsTask is one task of `agentctl ls --json`.
type lsTask struct {
	ID         string `json:"id"`
	Section    string `json:"section"`
	Label      string `json:"label"`
	AgentState string `json:"agent_state"`
	Confidence string `json:"confidence"`
	LastDetail string `json:"last_detail"`
	TaskState  string `json:"task_state"`
	Branch     string `json:"branch"`
	Worktree   string `json:"worktree"`
	Unread     bool   `json:"unread"`
	Model      string `json:"model"`
	Session    *struct {
		ID        int64  `json:"id"`
		Socket    string `json:"tmux_socket"`
		ServerPID int    `json:"tmux_server_pid"`
		SessionID string `json:"tmux_session_id"`
		Name      string `json:"tmux_session_name"`
		PaneID    string `json:"tmux_pane_id"`
		PanePID   int    `json:"pane_pid"`
	} `json:"session"`
	Messages map[string]int `json:"messages"`
}

func (tk lsTask) identity() domain.Identity {
	s := tk.Session
	return domain.Identity{Socket: s.Socket, ServerPID: s.ServerPID, SessionID: s.SessionID, PaneID: s.PaneID, PanePID: s.PanePID}
}

// tasks runs `agentctl ls --json --closed` (which also reconciles) and
// returns open and closed tasks by id.
func (r *rig) tasks() map[string]lsTask {
	r.t.Helper()
	out := map[string]lsTask{}
	for _, extra := range [][]string{nil, {"--closed"}} {
		stdout, errOut, code := r.agentctl(append([]string{"ls", "--json"}, extra...)...)
		var doc struct {
			Tasks []lsTask `json:"tasks"`
		}
		if code != 0 || json.Unmarshal([]byte(stdout), &doc) != nil {
			r.t.Fatalf("ls --json %v: exit %d %s\n%s", extra, code, errOut, stdout)
		}
		for _, tk := range doc.Tasks {
			out[tk.ID] = tk
		}
	}
	return out
}

func (r *rig) task(id string) lsTask {
	r.t.Helper()
	tk, ok := r.tasks()[id]
	if !ok {
		r.t.Fatalf("task %s not in ls --json", id)
	}
	return tk
}

// waitState polls the registry until task id's agent_state is want.
func (r *rig) waitState(id, want string) lsTask {
	r.t.Helper()
	var last lsTask
	testenv.WaitFor(r.t, stateWait, id+" "+want, func() (bool, string) {
		last = r.task(id)
		return last.AgentState == want, last.AgentState + " / " + last.LastDetail
	})
	return last
}

// store opens the registry read side for assertions (tests may; the TUI may not).
func (r *rig) store() *store.Store {
	r.t.Helper()
	st, err := store.Open(context.Background(), r.paths.DB, store.Options{})
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = st.Close() })
	return st
}

func (r *rig) messages(task string) []store.Message {
	r.t.Helper()
	st, err := store.Open(context.Background(), r.paths.DB, store.Options{})
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	ms, err := st.Messages().ForTask(context.Background(), task, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	return ms
}

// waitMessage waits until task has exactly one message, in state want.
func (r *rig) waitMessage(task string, want domain.MessageState) store.Message {
	r.t.Helper()
	var last []store.Message
	testenv.WaitFor(r.t, stateWait, task+"'s message "+string(want), func() (bool, string) {
		last = r.messages(task)
		return len(last) == 1 && last[0].State == want, describeMessages(last)
	})
	return last[0]
}

func describeMessages(ms []store.Message) string {
	var b strings.Builder
	for _, m := range ms {
		b.WriteString(string(m.State) + " ")
	}
	return b.String()
}

func (r *rig) eventCount(typ string) int {
	r.t.Helper()
	st, err := store.Open(context.Background(), r.paths.DB, store.Options{})
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	n, err := st.Events().CountType(context.Background(), typ)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// input is one entry fakeagent logged for a received input.
type input struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"` // paste | submit | key
	State string `json:"state_at_receipt"`
}

func (r *rig) inputs(agent string) []input {
	r.t.Helper()
	b, err := os.ReadFile(r.inputLog(agent))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	var out []input
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var in input
		if err := json.Unmarshal([]byte(l), &in); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, in)
	}
	return out
}

func (r *rig) pastes(agent string) []input {
	r.t.Helper()
	var out []input
	for _, in := range r.inputs(agent) {
		if in.Kind == "paste" {
			out = append(out, in)
		}
	}
	return out
}

// assertNothingWhileBusy: no paste and no submitted text ever reached the
// agent while it was not ready (single keys, the user's own y in a dialog,
// are the user's and are allowed).
func (r *rig) assertNothingWhileBusy(agent string) {
	r.t.Helper()
	for _, in := range r.inputs(agent) {
		if in.Kind != "key" && in.State != "ready" {
			r.t.Errorf("%s received input while %s: %+v", agent, in.State, in)
		}
	}
}

// keysToPane types into an agent's pane as the user would.
func (r *rig) keysToPane(pane, literal string, enter bool) {
	r.t.Helper()
	r.srv.Run(r.t, "send-keys", "-t", pane, "-l", literal)
	if enter {
		r.srv.Run(r.t, "send-keys", "-t", pane, "Enter")
	}
}

// paneAlive reports whether the pane with the recorded identity is still
// the same live process (pane id, pane pid and server pid all match).
func (r *rig) paneAlive(id domain.Identity) bool {
	r.t.Helper()
	snap, err := r.srv.Client().Snapshot(context.Background())
	if err != nil {
		return false
	}
	p, ok := snap.Find(id.PaneID)
	return ok && snap.ServerPID == id.ServerPID && p.PanePID == id.PanePID && !p.Dead
}

// checkHooksFailedOpen (invariant 7): every hook any fakeagent ran exited 0,
// wrote nothing on stdout and finished in under 2 s; hook-errors.log is empty.
func (r *rig) checkHooksFailedOpen() {
	files, _ := filepath.Glob(filepath.Join(r.logs, "*.hooks.jsonl"))
	n := 0
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			r.t.Error(err)
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var h struct {
				Event    string `json:"event"`
				Exit     int    `json:"exit"`
				Stdout   string `json:"stdout"`
				Millis   int64  `json:"ms"`
				TimedOut bool   `json:"timed_out"`
			}
			if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
				r.t.Errorf("%s: %v", file, err)
				continue
			}
			n++
			if h.Exit != 0 || h.Stdout != "" || h.TimedOut || h.Millis >= hookLimit.Milliseconds() {
				r.t.Errorf("hook %s failed closed: exit %d stdout %q %dms", h.Event, h.Exit, h.Stdout, h.Millis)
			}
		}
		_ = f.Close()
	}
	if len(files) > 0 && n == 0 {
		r.t.Error("fakeagent ran no hooks")
	}
	if b, _ := os.ReadFile(filepath.Join(r.paths.Logs, "hook-errors.log")); len(b) != 0 {
		r.t.Errorf("hook-errors.log:\n%s", b)
	}
}

// waitDeliversDone lets this test's detached deliver processes finish before
// the temp dirs go away. They always exit within AckWindow + settle.
func (r *rig) waitDeliversDone() {
	testenv.Eventually(30*time.Second, func() (bool, string) {
		out, _ := exec.Command("pgrep", "-f", r.bin+" deliver").Output()
		return strings.TrimSpace(string(out)) == "", string(out)
	})
}

// stateChanges is task's agent.state_changed history as "from>to".
func (r *rig) stateChanges(task string) []string {
	r.t.Helper()
	st, err := store.Open(context.Background(), r.paths.DB, store.Options{})
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	evs, err := st.Events().ForTask(context.Background(), task)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Type != domain.EventAgentStateChanged {
			continue
		}
		var p struct{ From, To string }
		_ = json.Unmarshal(e.Payload, &p)
		out = append(out, p.From+">"+p.To)
	}
	return out
}
