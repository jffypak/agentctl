package e2e

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
)

// TestAC4_QueuedWhileBusyNotInjected — spec §11: "A message queued while
// an agent is busy is not injected into its active terminal unexpectedly."
//
// Two cases, both composed in the real TUI and both asserted against
// fakeagent's own record of every input it received and the agent state at
// that moment:
//   - working: the prompt is queued "agent working", nothing reaches the
//     agent during the turn, and it is pasted only after the turn's Stop hook
//     (the claim's cause is that Stop), then acknowledged by hash;
//   - #ask: the agent sits in a permission dialog (where a stray Enter would
//     approve). The prompt is queued "awaiting input" and survives explicit
//     drains and TUI ticks unpasted; only after the user answers the dialog
//     and the turn ends is it pasted.
//
// The hook and deliver processes that did the work have all exited and both
// agents are still alive (invariant 2).
func TestAC4_QueuedWhileBusyNotInjected(t *testing.T) {
	t.Parallel()
	r := newRig(t, "FAKEAGENT_WORK_MS=4000")
	r.newAgent("busy", "a long first turn")
	r.newAgent("asker", "#ask")
	r.waitState("busy", "working")
	asker := r.waitState("asker", "awaiting_input")

	ui := r.startTUI("ui", nil)

	// working: compose while the turn runs
	ui.Select(t, "busy")
	ui.Keys(t, "Enter")
	ui.WaitText(t, "busy  RUNNING · working")
	ui.Keys(t, "p")
	ui.WaitText(t, "prompt  busy", "will be queued · agent working")
	ui.Type(t, "queued while working")
	ui.Keys(t, "C-s")
	ui.WaitText(t, "acknowledged (hash) · queued while working")
	m := r.waitMessage("busy", domain.MsgAcknowledged)
	if m.AckMatch != domain.AckHash {
		t.Fatalf("busy message = %+v", m)
	}
	r.assertNothingWhileBusy("busy")
	if p := r.pastes("busy"); len(p) != 1 || p[0].Text != "queued while working" || p[0].State != "ready" {
		t.Fatalf("busy received %+v", r.inputs("busy"))
	}
	r.assertClaimedOnStopAfterQueue("busy")
	ui.Keys(t, "Escape")
	ui.WaitText(t, "↵ open")

	// #ask: the agent is in a permission dialog
	ui.Select(t, "asker")
	ui.Keys(t, "Enter")
	ui.WaitText(t, "asker  NEEDS YOU · awaiting input")
	ui.Keys(t, "p")
	ui.WaitText(t, "prompt  asker", "will be queued · awaiting input")
	ui.Type(t, "never into the dialog")
	ui.Keys(t, "C-s")
	ui.WaitText(t, "queued · awaiting input · never into the dialog")
	// explicit drains (each runs the full delivery path) on top of the TUI's
	// own drain tick: none may paste into the dialog
	for range 3 {
		if out, errOut, code := r.agentctl("deliver", "--session", strconv.FormatInt(asker.Session.ID, 10)); code != 0 {
			t.Fatalf("deliver: %d %s %s", code, out, errOut)
		}
	}
	if p := r.pastes("asker"); len(p) != 0 {
		t.Fatalf("pasted into a permission dialog: %+v", p)
	}
	if ms := r.messages("asker"); len(ms) != 1 || ms[0].State != domain.MsgQueued {
		t.Fatalf("asker messages: %s", describeMessages(ms))
	}
	if tk := r.task("asker"); tk.AgentState != "awaiting_input" || tk.Section != "needs_you" {
		t.Fatalf("asker = %+v", tk)
	}
	r.keysToPane(asker.Session.PaneID, "y", false) // the user answers the dialog in the pane
	ui.WaitText(t, "acknowledged (hash) · never into the dialog")
	r.waitMessage("asker", domain.MsgAcknowledged)
	r.assertNothingWhileBusy("asker")
	if p := r.pastes("asker"); len(p) != 1 || p[0].Text != "never into the dialog" || p[0].State != "ready" {
		t.Fatalf("asker received %+v", r.inputs("asker"))
	}
	r.assertClaimedOnStopAfterQueue("asker")

	ui.Keys(t, "Escape")
	ui.WaitText(t, "↵ open")
	ui.Keys(t, "q")
	if st := ui.WaitExit(t); st != "0" {
		t.Fatalf("exit status %s", st)
	}
	// invariant 2: hooks and deliver processes came and went; agents stay
	r.waitDeliversDone()
	for _, name := range []string{"busy", "asker"} {
		if tk := r.task(name); !r.paneAlive(tk.identity()) {
			t.Fatalf("%s's agent died", name)
		}
	}
}

// assertClaimedOnStopAfterQueue: the task's only message was claimed because
// of a Stop hook recorded after it was queued (the delivery waited for the
// turn to end instead of racing it).
func (r *rig) assertClaimedOnStopAfterQueue(task string) {
	r.t.Helper()
	evs, err := r.store().Events().ForTask(context.Background(), task)
	if err != nil {
		r.t.Fatal(err)
	}
	byID := map[int64]int{}
	for i, e := range evs {
		byID[e.ID] = i
	}
	var queuedID, claimedCause int64
	for _, e := range evs {
		switch e.Type {
		case domain.EventMessageQueued:
			queuedID = e.ID
		case domain.EventMessageClaimed:
			claimedCause = e.CauseID
		}
	}
	i, ok := byID[claimedCause]
	if queuedID == 0 || !ok {
		r.t.Fatalf("%s: queued %d, claimed cause %d not found", task, queuedID, claimedCause)
	}
	cause := evs[i]
	var p struct {
		Event string `json:"event"`
	}
	_ = json.Unmarshal(cause.Payload, &p)
	if cause.Type != domain.EventHookReceived || p.Event != "Stop" || cause.ID < queuedID {
		r.t.Fatalf("%s: claimed because of %s %s (event %d), queued at event %d", task, cause.Type, cause.Payload, cause.ID, queuedID)
	}
}
