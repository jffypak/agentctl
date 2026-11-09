package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

// trustWait bounds detection of the folder-trust dialog. It is well under
// the default StartingTimeout (30 s), and the detail it waits for is not the
// timeout's, so a pass proves the dialog was read, not timed out.
const trustWait = 20 * time.Second

// waitFolderTrust waits until `ls` (which reconciles: one poll) shows task
// in NEEDS YOU with the folder-trust detail.
func (r *rig) waitFolderTrust(task string) lsTask {
	r.t.Helper()
	var last lsTask
	testenv.WaitFor(r.t, trustWait, task+" folder trust", func() (bool, string) {
		last = r.task(task)
		return last.LastDetail == domain.FolderTrustDetail, last.AgentState + " / " + last.Section + " / " + last.LastDetail
	})
	if last.AgentState != string(domain.StateUnknown) || last.Section != string(domain.SectionNeedsYou) {
		r.t.Fatalf("%s: %s in %s, want unknown in NEEDS YOU", task, last.AgentState, last.Section)
	}
	return last
}

// TestFolderTrustDialogSurfacesAndSessionStartClearsIt: fakeagent shows a
// Claude-style trust dialog and fires no hook. agentctl surfaces it in
// NEEDS YOU with "folder trust: attach to answer", queues a send instead of
// typing into the dialog, and never sends the dialog a key. The test (the
// user) answers it; SessionStart then maps through the normal hook path and
// the queued prompt is delivered once the agent is ready.
func TestFolderTrustDialogSurfacesAndSessionStartClearsIt(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_TRUST_DIALOG=1", "FAKEAGENT_WORK_MS=100")
	r.newAgent("alpha", "hello")
	tk := r.waitFolderTrust("alpha")

	out, errOut, code := r.agentctl("send", "alpha", "after trust")
	if code != 0 || !strings.Contains(out, "queued") {
		t.Fatalf("send during the dialog: exit %d %q %q, want queued", code, out, errOut)
	}
	if in := r.inputs("alpha"); len(in) != 0 {
		t.Fatalf("agentctl wrote into the trust dialog: %+v", in)
	}

	r.keysToPane(tk.Session.PaneID, "1", false) // the user answers; agentctl never does
	r.waitMessage("alpha", domain.MsgAcknowledged)
	ready := r.waitState("alpha", "ready")
	if ready.LastDetail == domain.FolderTrustDetail {
		t.Fatalf("the trust detail survived SessionStart: %+v", ready)
	}
	var trustInputs []input
	for _, in := range r.inputs("alpha") {
		if in.State == "trust" {
			trustInputs = append(trustInputs, in)
		}
	}
	if len(trustInputs) != 1 || trustInputs[0].Text != "1" || trustInputs[0].Kind != "key" {
		t.Fatalf("the dialog received %+v, want only the user's key", trustInputs)
	}
	if p := r.pastes("alpha"); len(p) != 1 || p[0].Text != "after trust" || p[0].State != "ready" {
		t.Fatalf("pastes = %+v", p)
	}
	r.assertNothingWhileBusy("alpha")
	// out of unknown by whatever the hook mapping says (SessionStart -> ready
	// today; m3-goal-start-race may keep a goal launch non-deliverable longer)
	if ch := strings.Join(r.stateChanges("alpha"), " "); !strings.HasPrefix(ch, "starting>unknown unknown>") {
		t.Fatalf("state changes = %s", ch)
	}
}

// TestFolderTrustDialogDeclinedSurfacesExit: "No, exit" ends the agent
// without any hook; the dead pane is still surfaced (failed: NEEDS YOU).
func TestFolderTrustDialogDeclinedSurfacesExit(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_TRUST_DIALOG=1")
	r.newAgent("beta", "hello")
	tk := r.waitFolderTrust("beta")
	r.keysToPane(tk.Session.PaneID, "2", false)
	got := r.waitState("beta", "failed")
	if got.Section != string(domain.SectionNeedsYou) || got.LastDetail != "exit 1" {
		t.Fatalf("declined: %s %q in %s", got.AgentState, got.LastDetail, got.Section)
	}
}
