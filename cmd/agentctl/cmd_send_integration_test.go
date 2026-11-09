package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// newPromptIT is newHookIT with chosen fakeagent / deliver knobs. Every
// FAKEAGENT_* and AGENTCTL_* knob travels in the agent argv (tmux panes get
// the server's environment), so the hooks and the deliver processes they
// spawn inherit it too. strict adds the hook fail-open check.
func newPromptIT(t *testing.T, strict bool, knobs ...string) *hookIT {
	t.Helper()
	bin, fake := testenv.BuildAgentctl(t), testenv.BuildFakeAgent(t)
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	logs := t.TempDir()
	argv := append([]string{"/usr/bin/env", "FAKEAGENT_LOG=" + filepath.Join(logs, "input.jsonl"),
		"FAKEAGENT_HOOK_LOG=" + filepath.Join(logs, "hooks.jsonl"), "AGENTCTL_DELIVER_SETTLE=50ms"}, knobs...)
	agentCmd, _ := json.Marshal(append(argv, fake))
	env := srv.Env.With("AGENTCTL_AGENT_CMD=" + string(agentCmd))
	p, err := paths.Resolve(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	it := &hookIT{t: t, bin: bin, fake: fake, env: env, srv: srv, repo: testenv.TempRepo(t, env), logs: logs, paths: p}
	if strict {
		t.Cleanup(it.checkHooksFailedOpen)
	}
	t.Cleanup(func() { it.waitDeliversDone() })
	return it
}

// waitDeliversDone lets detached deliver processes finish before the temp
// dirs go away (they always exit within AckWindow + settle).
func (it *hookIT) waitDeliversDone() {
	testenv.Eventually(20*time.Second, func() (bool, string) {
		out, _ := exec.Command("pgrep", "-f", it.bin+" deliver").Output()
		return strings.TrimSpace(string(out)) == "", string(out)
	})
}

func (it *hookIT) inputs() []fakeIn {
	it.t.Helper()
	b, err := os.ReadFile(filepath.Join(it.logs, "input.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		it.t.Fatal(err)
	}
	var out []fakeIn
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var in fakeIn
		if err := json.Unmarshal([]byte(l), &in); err != nil {
			it.t.Fatal(err)
		}
		out = append(out, in)
	}
	return out
}

type fakeIn struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	State string `json:"state_at_receipt"`
}

func (it *hookIT) pastes() []fakeIn {
	it.t.Helper()
	var out []fakeIn
	for _, in := range it.inputs() {
		if in.Kind == "paste" {
			out = append(out, in)
		}
	}
	return out
}

// assertNothingWhileBusy is the core claim of the card: no paste (and no
// submitted text) ever reached the agent while it was not ready.
func (it *hookIT) assertNothingWhileBusy() {
	it.t.Helper()
	for _, in := range it.inputs() {
		if in.Kind != "key" && in.State != "ready" {
			it.t.Errorf("input received while %s: %+v", in.State, in)
		}
	}
}

func (it *hookIT) store() *store.Store {
	it.t.Helper()
	st, err := store.Open(context.Background(), it.paths.DB, store.Options{})
	if err != nil {
		it.t.Fatal(err)
	}
	it.t.Cleanup(func() { _ = st.Close() })
	return st
}

func (it *hookIT) msg(id int64) store.Message {
	it.t.Helper()
	st, err := store.Open(context.Background(), it.paths.DB, store.Options{})
	if err != nil {
		it.t.Fatal(err)
	}
	defer st.Close()
	m, err := st.Messages().Get(context.Background(), id)
	if err != nil {
		it.t.Fatal(err)
	}
	return m
}

func (it *hookIT) waitMsg(id int64, want domain.MessageState, timeout time.Duration) store.Message {
	it.t.Helper()
	var m store.Message
	testenv.WaitFor(it.t, timeout, "message "+strconv.FormatInt(id, 10)+" "+string(want), func() (bool, string) {
		m = it.msg(id)
		return m.State == want, string(m.State)
	})
	return m
}

// send runs `agentctl send` and returns the message id and label it printed.
func (it *hookIT) send(task, body string) (int64, string) {
	it.t.Helper()
	out, errOut, code := it.agentctl("send", task, body)
	if code != 0 {
		it.t.Fatalf("send: %d %s %s", code, out, errOut)
	}
	id, label, ok := strings.Cut(strings.TrimSpace(out), " ")
	n, err := strconv.ParseInt(strings.TrimPrefix(id, "#"), 10, 64)
	if !ok || err != nil {
		it.t.Fatalf("send output %q", out)
	}
	return n, label
}

func (it *hookIT) deliverLog() string {
	b, _ := os.ReadFile(filepath.Join(it.paths.Logs, "deliver.log"))
	return string(b)
}

func (it *hookIT) buffers() []string {
	it.t.Helper()
	bufs, err := it.srv.Client().ListBuffers(context.Background())
	if err != nil {
		it.t.Fatal(err)
	}
	return bufs
}

// TestPromptQueuedWhileWorkingDeliveredOnStop (WORK_MS=3000): a send during
// the turn is queued "agent working"; nothing reaches the agent while it is
// not ready; on Stop the hook spawns deliver, which pastes, and the
// UserPromptSubmit acknowledges it by hash. The hook still returns in < 2 s.
func TestPromptQueuedWhileWorkingDeliveredOnStop(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, true, "FAKEAGENT_WORK_MS=3000")
	if _, errOut, code := it.agentctl("new", "t1", "--goal", "first turn"); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	it.waitState("t1", "working", 5*time.Second)
	body := "now add the docs\nand run the tests"
	id, label := it.send("t1", body)
	if label != "queued · agent working" {
		t.Fatalf("label = %q", label)
	}
	m := it.waitMsg(id, domain.MsgAcknowledged, 15*time.Second)
	if m.AckMatch != domain.AckHash {
		t.Fatalf("ack = %+v", m)
	}
	it.assertNothingWhileBusy()
	if p := it.pastes(); len(p) != 1 || p[0].Text != body || p[0].State != "ready" {
		t.Fatalf("pastes = %+v", p)
	}
	if b := it.buffers(); len(b) != 0 {
		t.Fatalf("tmux buffers after delivery: %v", b)
	}
	testenv.WaitFor(t, 0, "deliver.log records the ack", func() (bool, string) {
		lg := it.deliverLog()
		return strings.Contains(lg, "acknowledged (hash)"), lg
	})
	// every message event carries a cause_id that is a hook.received
	st := it.store()
	evs, _ := st.Events().ForTask(context.Background(), "t1")
	byID := map[int64]string{}
	for _, e := range evs {
		byID[e.ID] = e.Type
	}
	for _, e := range evs {
		switch e.Type {
		case domain.EventMessageClaimed, domain.EventMessageSent, domain.EventMessageAcknowledged:
			if byID[e.CauseID] != domain.EventHookReceived {
				t.Errorf("%s cause_id %d is %q, want a hook.received", e.Type, e.CauseID, byID[e.CauseID])
			}
		}
	}
}

// TestPromptNeverPastedIntoAPermissionDialog: #ask holds a permission prompt;
// a send is queued "awaiting input" and nothing is pasted until y + Stop.
func TestPromptNeverPastedIntoAPermissionDialog(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, true, "FAKEAGENT_WORK_MS=200")
	if _, errOut, code := it.agentctl("new", "t1", "--goal", "#ask"); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	tk := it.waitState("t1", "awaiting_input", 5*time.Second)
	id, label := it.send("t1", "do the next thing")
	if label != "queued · awaiting input" {
		t.Fatalf("label = %q", label)
	}
	for range 3 { // drain ticks while the dialog is up
		it.agentctl("deliver", "--session", strconv.FormatInt(tk.Session.ID, 10))
		it.task("t1")
	}
	if p := it.pastes(); len(p) != 0 {
		t.Fatalf("pasted into the dialog: %+v", p)
	}
	if m := it.msg(id); m.State != domain.MsgQueued {
		t.Fatalf("message = %s", m.State)
	}
	it.keys(tk.Session.TmuxPaneID, "y", false)
	it.waitMsg(id, domain.MsgAcknowledged, 10*time.Second)
	it.assertNothingWhileBusy()
	if p := it.pastes(); len(p) != 1 || p[0].State != "ready" {
		t.Fatalf("pastes = %+v", p)
	}
}

// TestPlaceholderPasteAcknowledgedByTiming: Claude reports a paste as
// "[Pasted text #1]"; the submit right after the paste acknowledges it.
func TestPlaceholderPasteAcknowledgedByTiming(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, true, "FAKEAGENT_WORK_MS=200", "FAKEAGENT_PASTE_PLACEHOLDER=1")
	if _, errOut, code := it.agentctl("new", "t1"); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	it.waitState("t1", "ready", 5*time.Second)
	id, label := it.send("t1", "a large\nmulti-line\npaste")
	if label != "sent · awaiting ack" {
		t.Fatalf("label = %q", label)
	}
	if m := it.waitMsg(id, domain.MsgAcknowledged, 10*time.Second); m.AckMatch != domain.AckTiming {
		t.Fatalf("ack = %+v", m)
	}
	it.assertNothingWhileBusy()
}

// TestUnacknowledgedPasteBecomesUnconfirmedAndIsNeverRepasted: an adopted
// pane whose settings lack the UserPromptSubmit hook never acknowledges; the
// deliver process marks the message unconfirmed after the (shortened)
// AckWindow, the task shows in NEEDS YOU, and later Stops never paste it again.
func TestUnacknowledgedPasteBecomesUnconfirmedAndIsNeverRepasted(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, false)
	snippet, _, code := it.agentctl("hooks-snippet")
	if code != 0 {
		t.Fatal("hooks-snippet failed")
	}
	var settings map[string]map[string]any
	if err := json.Unmarshal([]byte(snippet), &settings); err != nil {
		t.Fatal(err)
	}
	delete(settings["hooks"], "UserPromptSubmit")
	b, _ := json.Marshal(settings)
	file := filepath.Join(it.logs, "no-submit-settings.json")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := it.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: "by-hand", Dir: it.repo.Root,
		Argv: []string{"/usr/bin/env", "FAKEAGENT_WORK_MS=1500", "FAKEAGENT_LOG=" + filepath.Join(it.logs, "input.jsonl"),
			"AGENTCTL_ACK_WINDOW=1s", "AGENTCTL_DELIVER_SETTLE=50ms", it.fake, "--settings", file},
	})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, 0, "fakeagent prompt", func() (bool, string) {
		out, _ := it.srv.TryRun("capture-pane", "-p", "-t", created.PaneID)
		return strings.HasSuffix(strings.TrimSpace(out), ">"), out
	})
	if _, errOut, code := it.agentctl("adopt", created.PaneID, "--name", "ad1"); code != 0 {
		t.Fatalf("adopt: %s", errOut)
	}
	it.keys(created.PaneID, "warm up", true) // PreToolUse -> working (hook), Stop -> ready
	it.waitState("ad1", "working", 5*time.Second)
	it.waitState("ad1", "ready", 5*time.Second)
	it.keys(created.PaneID, "second turn", true)
	it.waitState("ad1", "working", 5*time.Second)
	id, label := it.send("ad1", "never acknowledged")
	if label != "queued · agent working" {
		t.Fatalf("label = %q", label)
	}
	it.waitMsg(id, domain.MsgUnconfirmed, 15*time.Second)
	out, _, _ := it.agentctl("ls", "--json")
	if !strings.Contains(out, `"section": "needs_you"`) || !strings.Contains(out, "message unconfirmed") {
		t.Fatalf("ls --json:\n%s", out)
	}
	testenv.WaitFor(t, 0, "deliver.log records unconfirmed", func() (bool, string) {
		lg := it.deliverLog()
		return strings.Contains(lg, "unconfirmed"), lg
	})
	// more turns: Stop after Stop, the unconfirmed message is never pasted again
	for range 2 {
		it.waitState("ad1", "ready", 10*time.Second)
		it.keys(created.PaneID, "another turn", true)
		it.waitState("ad1", "working", 5*time.Second)
	}
	it.waitState("ad1", "ready", 10*time.Second)
	n := 0
	for _, p := range it.pastes() {
		if p.Text == "never acknowledged" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("pasted %d times", n)
	}
	it.assertNothingWhileBusy()
}

// TestUnknownAdoptedSessionNeverPasted: an adopted pane without hooks stays
// unknown; a send is queued "attach to deliver", never pasted across three
// drain ticks, and the task is in NEEDS YOU.
func TestUnknownAdoptedSessionNeverPasted(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, false)
	file := filepath.Join(it.logs, "empty-settings.json")
	if err := os.WriteFile(file, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := it.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: "plain", Dir: it.repo.Root,
		Argv: []string{"/usr/bin/env", "FAKEAGENT_LOG=" + filepath.Join(it.logs, "input.jsonl"), it.fake, "--settings", file},
	})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, 0, "fakeagent prompt", func() (bool, string) {
		out, _ := it.srv.TryRun("capture-pane", "-p", "-t", created.PaneID)
		return strings.HasSuffix(strings.TrimSpace(out), ">"), out
	})
	if _, errOut, code := it.agentctl("adopt", created.PaneID, "--name", "ad1"); code != 0 {
		t.Fatalf("adopt: %s", errOut)
	}
	_, label := it.send("ad1", "hello")
	if label != "queued · attach to deliver" {
		t.Fatalf("label = %q", label)
	}
	tk := it.task("ad1")
	for range 3 {
		it.agentctl("deliver", "--session", strconv.FormatInt(tk.Session.ID, 10))
	}
	out, _, _ := it.agentctl("ls", "--json")
	if !strings.Contains(out, `"section": "needs_you"`) || !strings.Contains(out, "queued · attach to deliver") {
		t.Fatalf("ls --json:\n%s", out)
	}
	if in := it.inputs(); len(in) != 0 {
		t.Fatalf("pasted into an unknown agent: %+v", in)
	}
}

func TestSendRejectsStoppedAgentAndCancelsQueuedOnly(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, true, "FAKEAGENT_WORK_MS=2000")
	if _, errOut, code := it.agentctl("new", "t1", "--goal", "long turn"); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	tk := it.waitState("t1", "working", 5*time.Second)
	id, _ := it.send("t1", "cancel me")
	out, errOut, code := it.agentctl("send", "--cancel", strconv.FormatInt(id, 10))
	if code != 0 || strings.TrimSpace(out) != "#"+strconv.FormatInt(id, 10)+" cancelled" {
		t.Fatalf("cancel: %d %q %q", code, out, errOut)
	}
	if _, errOut, code := it.agentctl("send", "--cancel", strconv.FormatInt(id, 10)); code != 1 || !strings.Contains(errOut, "only queued") {
		t.Fatalf("cancel twice: %d %q", code, errOut)
	}
	for _, args := range [][]string{{"send"}, {"send", "t1"}, {"send", "--cancel", "1", "t1"}} {
		if _, _, code := it.agentctl(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	it.waitState("t1", "ready", 10*time.Second)
	it.keys(tk.Session.TmuxPaneID, "#exit 0", true)
	it.waitState("t1", "stopped", 5*time.Second)
	before := it.eventCount(domain.EventMessageQueued)
	if _, errOut, code := it.agentctl("send", "t1", "too late"); code != 1 || !strings.Contains(errOut, "send rejected") || strings.Count(errOut, "\n") != 1 {
		t.Fatalf("send to stopped: %d %q", code, errOut)
	}
	if n := it.eventCount(domain.EventMessageQueued); n != before {
		t.Fatal("a rejected send stored a message")
	}
	if p := it.pastes(); len(p) != 0 {
		t.Fatalf("pastes = %+v", p)
	}
}

// TestConcurrentDrainsGoroutinesAndProcesses: 18 goroutines plus 2 `agentctl
// deliver` processes drain one ready session with two queued messages:
// exactly one paste and one sent message; the second waits for the next Stop.
func TestConcurrentDrainsGoroutinesAndProcesses(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, true, "FAKEAGENT_WORK_MS=1500", "AGENTCTL_ACK_WINDOW=3s")
	if _, errOut, code := it.agentctl("new", "t1"); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	tk := it.waitState("t1", "ready", 5*time.Second)
	ctx := context.Background()
	st := it.store()
	for _, b := range []string{"first", "second"} {
		if _, err := st.Messages().Enqueue(ctx, store.NewMessage{TaskID: "t1", SessionID: tk.Session.ID, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	ctl, err := controller.Open(ctx, controller.Config{Environ: it.env.Strings(), AgentctlPath: it.bin})
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	var delivered atomic.Int32
	var wg sync.WaitGroup
	procs := make([]*exec.Cmd, 2)
	for i := range procs {
		procs[i] = exec.Command(it.bin, "deliver", "--session", strconv.FormatInt(tk.Session.ID, 10))
		procs[i].Env = it.env.With("AGENTCTL_DELIVER_SETTLE=1ms", "AGENTCTL_ACK_WINDOW=3s").Strings()
		if err := procs[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for range 18 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dr, err := ctl.Drain(ctx, tk.Session.ID)
			if err != nil {
				t.Error(err)
			}
			if dr.Delivered {
				delivered.Add(1)
			}
		}()
	}
	wg.Wait()
	for _, p := range procs {
		_ = p.Wait()
	}
	testenv.WaitFor(t, 5*time.Second, "one paste", func() (bool, string) {
		return len(it.pastes()) >= 1, ""
	})
	var sent, queued int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE sent_at IS NOT NULL`).Scan(&sent)
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM messages WHERE state = 'queued'`).Scan(&queued)
	if p := it.pastes(); len(p) != 1 || p[0].Text != "first" || sent != 1 || queued != 1 {
		t.Fatalf("pastes %+v, %d sent, %d queued (in-process deliveries %d)", p, sent, queued, delivered.Load())
	}
	// the next Stop delivers the second one
	testenv.WaitFor(t, 15*time.Second, "second paste after the next Stop", func() (bool, string) {
		return len(it.pastes()) == 2, strconv.Itoa(len(it.pastes()))
	})
	it.assertNothingWhileBusy()
	if p := it.pastes(); p[1].Text != "second" {
		t.Fatalf("pastes = %+v", p)
	}
}

// TestLostHookMarkerBlocksDeliveryEndToEnd: the registry is write-locked
// while the user submits a prompt by hand, so the UserPromptSubmit and
// PreToolUse hooks fail past their busy timeout and the session keeps
// reading ready while the agent works. The hooks leave a marker; a send then
// queues "state stale", nothing is pasted, reconcile reads unknown/none, and
// the Stop that is recorded clears the marker and delivers.
func TestLostHookMarkerBlocksDeliveryEndToEnd(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, false, "FAKEAGENT_WORK_MS=4000", "AGENTCTL_ACK_WINDOW=3s")
	if _, errOut, code := it.agentctl("new", "t1"); code != 0 {
		t.Fatalf("new: %s", errOut)
	}
	tk := it.waitState("t1", "ready", 5*time.Second)
	marker := filepath.Join(it.paths.Hooks, "lost", domain.LostHookSessionKey(tk.Session.ID))

	st := it.store()
	conn, err := st.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	it.keys(tk.Session.TmuxPaneID, "typed by hand", true)
	testenv.WaitFor(t, 10*time.Second, "UserPromptSubmit and PreToolUse hooks lost", func() (bool, string) {
		b, _ := os.ReadFile(filepath.Join(it.logs, "hooks.jsonl"))
		return strings.Contains(string(b), `"event":"PreToolUse"`), string(b)
	})
	_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
	_ = conn.Close()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no lost-hook marker: %v", err)
	}
	if got := it.task("t1"); got.AgentState != "unknown" || got.Confidence != "none" {
		t.Fatalf("after reconcile = %+v", got) // ls reconciles
	}
	id, label := it.send("t1", "must not land mid-turn")
	if label != "queued · attach to deliver" && label != "queued · "+domain.LabelStale {
		t.Fatalf("label = %q", label)
	}
	if p := it.pastes(); len(p) != 0 {
		t.Fatalf("pasted while stale: %+v", p)
	}
	it.waitMsg(id, domain.MsgAcknowledged, 15*time.Second)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker not cleared by the next recorded hook: %v", err)
	}
	it.assertNothingWhileBusy()
}
