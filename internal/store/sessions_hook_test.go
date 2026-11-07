package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
)

func TestRecordHookAppliesTransitionWithCause(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)

	res, err := st.Sessions().RecordHook(ctx, HookInput{
		SessionID: sess.ID, Event: "SessionStart", AgentSessionID: "abc-123",
		State: domain.StateReady, Detail: "session started (startup)",
		Payload: map[string]any{"source": "startup"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.Refused || !res.Transition.Changed || res.Transition.From != domain.StateStarting {
		t.Fatalf("result = %+v", res)
	}
	got := res.Session
	if got.State != domain.StateReady || got.Confidence != domain.ConfidenceHook || got.AgentSessionID != "abc-123" ||
		got.LastDetail != "session started (startup)" {
		t.Fatalf("session = %+v", got)
	}
	evs, _ := st.Events().ForTask(ctx, "demo")
	var received, changed *Event
	for i := range evs {
		switch evs[i].Type {
		case domain.EventHookReceived:
			received = &evs[i]
		case domain.EventAgentStateChanged:
			changed = &evs[i]
		}
	}
	if received == nil || changed == nil || received.ID != res.ReceivedID || changed.CauseID != received.ID || received.SessionID != sess.ID {
		t.Fatalf("events = %+v", evs)
	}
	var p map[string]any
	_ = json.Unmarshal(received.Payload, &p)
	if p["event"] != "SessionStart" || p["source"] != "startup" {
		t.Fatalf("hook.received payload = %s", received.Payload)
	}
	_ = json.Unmarshal(changed.Payload, &p)
	if p["source"] != domain.SourceHook || p["hook_event"] != "SessionStart" || p["confidence"] != "hook" {
		t.Fatalf("agent.state_changed payload = %s", changed.Payload)
	}
}

func TestRecordHookRecordOnlyAndSelfMove(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateWorking, domain.ConfidenceHook, "tool: Bash", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	before, _ := st.Sessions().Get(ctx, sess.ID)
	stateEvents := func() int {
		evs, _ := st.Events().ForTask(ctx, "demo")
		n := 0
		for _, e := range evs {
			if e.Type == domain.EventAgentStateChanged {
				n++
			}
		}
		return n
	}
	n0 := stateEvents()

	// record only (SessionStart compact): no state write at all
	res, err := st.Sessions().RecordHook(ctx, HookInput{SessionID: sess.ID, Event: "SessionStart", Payload: map[string]any{"source": "compact"}})
	if err != nil || res.Applied || res.Refused || res.ReceivedID == 0 {
		t.Fatalf("record only = %+v %v", res, err)
	}
	if after, _ := st.Sessions().Get(ctx, sess.ID); after.State != domain.StateWorking || after.LastDetail != before.LastDetail || !after.StateSince.Equal(before.StateSince) {
		t.Fatalf("record-only hook changed the session: %+v", after)
	}

	// self move: detail refreshed, state_since kept, no state event
	res, err = st.Sessions().RecordHook(ctx, HookInput{SessionID: sess.ID, Event: "PostToolUse", State: domain.StateWorking, Detail: "tool done: Bash"})
	if err != nil || !res.Applied || res.Transition.Changed {
		t.Fatalf("self move = %+v %v", res, err)
	}
	if res.Session.LastDetail != "tool done: Bash" || !res.Session.StateSince.Equal(before.StateSince) {
		t.Fatalf("self move session = %+v", res.Session)
	}
	if n := stateEvents(); n != n0 {
		t.Fatalf("state events %d -> %d", n0, n)
	}
}

func TestRecordHookNeverRevivesTerminal(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateStopped, domain.ConfidenceHook, "session ended", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	res, err := st.Sessions().RecordHook(ctx, HookInput{SessionID: sess.ID, Event: "SessionStart", AgentSessionID: "new", State: domain.StateReady, Detail: "x"})
	if err != nil || !res.Refused || res.Applied || res.ReceivedID == 0 {
		t.Fatalf("hook on stopped = %+v %v", res, err)
	}
	if res.Session.State != domain.StateStopped || res.Session.AgentSessionID != "" {
		t.Fatalf("stopped session changed: %+v", res.Session)
	}
}

func TestRecordHookSanitizesDetailAndMissingSession(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	res, err := st.Sessions().RecordHook(ctx, HookInput{SessionID: sess.ID, Event: "Notification", State: domain.StateAwaitingInput, Detail: "permission \x1b]52;c;eA==\x07prompt"})
	if err != nil || res.Session.LastDetail != "permission prompt" {
		t.Fatalf("detail = %q %v", res.Session.LastDetail, err)
	}
	if _, err := st.Sessions().RecordHook(ctx, HookInput{SessionID: 999, Event: "Stop", State: domain.StateReady}); err == nil {
		t.Fatal("RecordHook on a missing session succeeded")
	}
	if n := countRows(t, st, "events"); n == 0 {
		t.Fatal("no events")
	}
}

func TestActiveByPane(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "a")
	seedTask(t, st, "b")
	a := seedSession(t, st, "a", domain.StateUnknown) // pane %1
	b := seedSession(t, st, "b", domain.StateStarting)
	if _, err := st.DB().Exec(`UPDATE sessions SET tmux_pane_id = '%2' WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.Sessions().ActiveByPane(ctx, "%1")
	if err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("ActiveByPane = %+v %v", got, err)
	}
	if _, err := st.Sessions().Transition(ctx, a.ID, domain.StateDisconnected, domain.ConfidencePane, "", domain.SourcePane); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Sessions().ActiveByPane(ctx, "%1"); len(got) != 0 {
		t.Fatalf("terminal session still active: %+v", got)
	}
}
