package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

// codexEnv is name's agent env with Codex = fakeagent --codex.
func (r *rig) codexEnv(name string, extra ...string) testenv.Env {
	argv := append([]string{"/usr/bin/env",
		"FAKEAGENT_LOG=" + r.inputLog(name),
		"FAKEAGENT_HOOK_LOG=" + filepath.Join(r.logs, name+".hooks.jsonl"),
		"AGENTCTL_DELIVER_SETTLE=50ms"}, append(r.knobs, extra...)...)
	cmd, _ := json.Marshal(append(argv, r.fake, "--codex"))
	return r.agentEnv(name).With("AGENTCTL_CODEX_CMD=" + string(cmd))
}

// newCodexAgent runs `agentctl new name --agent codex [--goal goal]`.
func (r *rig) newCodexAgent(name, goal string, extra ...string) {
	r.t.Helper()
	args := []string{"new", name, "--agent", "codex"}
	if goal != "" {
		args = append(args, "--goal", goal)
	}
	if out, errOut, code := r.agentctlEnv(r.codexEnv(name, extra...), args...); code != 0 {
		r.t.Fatalf("agentctl new %s --agent codex: exit %d\n%s%s", name, code, out, errOut)
	}
}

// A Codex agent launched with a goal works it, goes idle on Stop, and a
// queued prompt is delivered exactly once, never while it works.
func TestE2ECodexNewGoalDeliver(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=400")
	r.newCodexAgent("cx", "first task")
	r.waitState("cx", "ready")
	if out, errOut, code := r.agentctl("send", "cx", "second task"); code != 0 {
		t.Fatalf("send: %s %s", out, errOut)
	}
	r.waitMessage("cx", domain.MsgAcknowledged)
	r.waitState("cx", "ready")
	if p := r.pastes("cx"); len(p) != 1 || p[0].Text != "second task" {
		t.Fatalf("pastes = %+v", p)
	}
	r.assertNothingWhileBusy("cx")
	if tk := r.task("cx"); tk.Section != "idle" {
		t.Fatalf("section = %s", tk.Section)
	}
}

// A Codex permission prompt is NEEDS YOU; a queued send is not pasted into
// the dialog, and is delivered after the user answers and the turn ends.
func TestE2ECodexPermissionNoPaste(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=200")
	r.newCodexAgent("cx", "#ask")
	tk := r.waitState("cx", "awaiting_input")
	if tk.Section != "needs_you" {
		t.Fatalf("section = %s", tk.Section)
	}
	if _, errOut, code := r.agentctl("send", "cx", "after the dialog"); code != 0 {
		t.Fatalf("send: %s", errOut)
	}
	time.Sleep(300 * time.Millisecond) // a paste now would be the bug
	if p := r.pastes("cx"); len(p) != 0 {
		t.Fatalf("pasted into the permission dialog: %+v", p)
	}
	r.keysToPane(tk.Session.PaneID, "y", false)
	r.waitMessage("cx", domain.MsgAcknowledged)
	if p := r.pastes("cx"); len(p) != 1 || p[0].Text != "after the dialog" {
		t.Fatalf("pastes = %+v", p)
	}
	r.assertNothingWhileBusy("cx")
}

// Codex's hook-trust screen surfaces in NEEDS YOU; agentctl never answers it.
func TestE2ECodexHookTrustNeedsYou(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.newCodexAgent("cx", "", "FAKEAGENT_HOOK_TRUST=1")
	testenv.WaitFor(t, 20*time.Second, "hook trust detail", func() (bool, string) {
		r.agentctl("ls") // ls reconciles
		tk := r.task("cx")
		return tk.LastDetail == domain.HookTrustDetail, tk.AgentState + " " + tk.LastDetail
	})
	if tk := r.task("cx"); tk.Section != "needs_you" || tk.AgentState != "unknown" {
		t.Fatalf("task = %+v", tk)
	}
	for _, in := range r.inputs("cx") {
		t.Fatalf("agentctl typed into the trust screen: %+v", in)
	}
}

// A Codex agent without a goal fires no hook until the first prompt: it sits
// in INITIALSTATE, and a send there needs the user. (That it is never timed
// out into NEEDS YOU is controller.TestReconcileCodexNoGoalNoTimeout: the
// start timeout has no env knob for an e2e run.)
func TestE2ECodexNoGoalInitialState(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.newCodexAgent("cx", "")
	testenv.WaitFor(t, 10*time.Second, "codex prompt", func() (bool, string) {
		out, _ := r.srv.TryRun("capture-pane", "-p", "-t", r.task("cx").Session.PaneID)
		return strings.HasSuffix(strings.TrimSpace(out), ">"), out
	})
	time.Sleep(200 * time.Millisecond) // a hook fired now would break the Codex contract
	r.agentctl("ls")
	tk := r.task("cx")
	if tk.Section != "initial" || tk.AgentState != "starting" || tk.LastDetail != domain.DetailAwaitingFirstPrompt {
		t.Fatalf("task = %+v", tk)
	}
	if _, errOut, code := r.agentctl("send", "cx", "hello"); code != 0 {
		t.Fatalf("send: %s", errOut)
	}
	if tk := r.task("cx"); tk.Section != "needs_you" || !strings.Contains(tk.Label, "attach to deliver") {
		t.Fatalf("after send: %+v", tk)
	}
	if p := r.pastes("cx"); len(p) != 0 {
		t.Fatalf("pasted into a Codex with no hook yet: %+v", p)
	}
}

// An adopted pane that never sent a hook is INITIALSTATE, not UNKNOWN.
func TestE2EAdoptedNoHooksInitialState(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	out := r.srv.Run(t, "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "hand", "-c", r.repo.Root, "/bin/sleep", "600")
	if _, errOut, code := r.agentctl("adopt", strings.TrimSpace(out), "--name", "hand"); code != 0 {
		t.Fatalf("adopt: %s", errOut)
	}
	if tk := r.task("hand"); tk.Section != "initial" || tk.Label != "no events yet" {
		t.Fatalf("task = %+v", tk)
	}
}
