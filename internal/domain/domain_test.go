package domain

import (
	"fmt"
	"strings"
	"testing"
)

var allAgentStates = []AgentState{
	StateStarting, StateReady, StateWorking, StateAwaitingInput,
	StateUnknown, StateStopped, StateFailed, StateDisconnected,
}

var allMessageStates = []MessageState{
	MsgQueued, MsgDelivering, MsgSent, MsgAcknowledged,
	MsgUnconfirmed, MsgFailed, MsgCancelled,
}

func TestAgentStateTransitions(t *testing.T) {
	allowed := map[AgentState][]AgentState{
		StateStarting:      {StateReady, StateWorking, StateAwaitingInput, StateUnknown, StateStopped, StateFailed, StateDisconnected},
		StateReady:         {StateWorking, StateAwaitingInput, StateUnknown, StateStopped, StateFailed, StateDisconnected},
		StateWorking:       {StateReady, StateAwaitingInput, StateUnknown, StateStopped, StateFailed, StateDisconnected},
		StateAwaitingInput: {StateReady, StateWorking, StateUnknown, StateStopped, StateFailed, StateDisconnected},
		StateUnknown:       {StateReady, StateWorking, StateAwaitingInput, StateStopped, StateFailed, StateDisconnected},
		StateStopped:       {},
		StateFailed:        {},
		StateDisconnected:  {},
	}
	for _, from := range allAgentStates {
		for _, to := range allAgentStates {
			want := from == to // self-transition is an allowed no-op
			for _, a := range allowed[from] {
				if a == to {
					want = true
				}
			}
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s -> %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTerminalStatesAreSticky(t *testing.T) {
	for _, s := range []AgentState{StateStopped, StateFailed, StateDisconnected} {
		if !s.IsTerminal() {
			t.Errorf("%s.IsTerminal() = false", s)
		}
		for _, hookState := range []AgentState{StateReady, StateWorking, StateAwaitingInput, StateStarting, StateUnknown} {
			if CanTransition(s, hookState) {
				t.Errorf("terminal %s must not move to %s", s, hookState)
			}
		}
	}
	for _, s := range []AgentState{StateStarting, StateReady, StateWorking, StateAwaitingInput, StateUnknown} {
		if s.IsTerminal() {
			t.Errorf("%s.IsTerminal() = true", s)
		}
	}
}

func TestUnknownAcceptsHookStates(t *testing.T) {
	for _, to := range []AgentState{StateReady, StateWorking, StateAwaitingInput} {
		if !CanTransition(StateUnknown, to) {
			t.Errorf("unknown -> %s must be allowed (hook evidence)", to)
		}
	}
	// lost-hook safety (m2-prompt-queue): reconcile downgrades a live state
	// whose hook was lost; terminal states stay sticky.
	for _, from := range []AgentState{StateReady, StateWorking, StateAwaitingInput} {
		if !CanTransition(from, StateUnknown) {
			t.Errorf("%s -> unknown must be allowed (lost-hook downgrade)", from)
		}
	}
}

func TestNothingReturnsToStarting(t *testing.T) {
	for _, from := range allAgentStates {
		if from != StateStarting && CanTransition(from, StateStarting) {
			t.Errorf("%s -> starting must not be allowed", from)
		}
	}
}

func TestCanTransitionRejectsInvalidStates(t *testing.T) {
	if CanTransition("bogus", StateReady) || CanTransition(StateReady, "bogus") {
		t.Error("invalid states must never transition")
	}
}

func TestMessageStateTransitions(t *testing.T) {
	allowed := map[MessageState][]MessageState{
		MsgQueued:       {MsgDelivering, MsgCancelled, MsgFailed},
		MsgDelivering:   {MsgQueued, MsgSent, MsgUnconfirmed, MsgFailed},
		MsgSent:         {MsgAcknowledged, MsgUnconfirmed},
		MsgUnconfirmed:  {MsgAcknowledged},
		MsgAcknowledged: {},
		MsgFailed:       {},
		MsgCancelled:    {},
	}
	for _, from := range allMessageStates {
		for _, to := range allMessageStates {
			want := false
			for _, a := range allowed[from] {
				if a == to {
					want = true
				}
			}
			if got := CanTransitionMessage(from, to); got != want {
				t.Errorf("CanTransitionMessage(%s -> %s) = %v, want %v", from, to, got, want)
			}
		}
	}
	if CanTransitionMessage("bogus", MsgQueued) {
		t.Error("invalid message state must not transition")
	}
}

func TestTaskStateTransitions(t *testing.T) {
	cases := []struct {
		from, to TaskState
		want     bool
	}{
		{TaskRunning, TaskClosed, true},
		{TaskClosed, TaskRunning, false},
		{TaskRunning, TaskRunning, false},
		{TaskClosed, TaskClosed, false},
		{"bogus", TaskClosed, false},
	}
	for _, c := range cases {
		if got := CanTransitionTask(c.from, c.to); got != c.want {
			t.Errorf("CanTransitionTask(%s -> %s) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestEnumValidity(t *testing.T) {
	for _, s := range allAgentStates {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	for _, s := range allMessageStates {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	for _, c := range []Confidence{ConfidenceHook, ConfidencePane, ConfidenceNone} {
		if !c.Valid() {
			t.Errorf("%s should be valid", c)
		}
	}
	for _, o := range []Origin{OriginLaunched, OriginAdopted} {
		if !o.Valid() {
			t.Errorf("%s should be valid", o)
		}
	}
	for _, ts := range []TaskState{TaskRunning, TaskClosed} {
		if !ts.Valid() {
			t.Errorf("%s should be valid", ts)
		}
	}
	for _, a := range []AckMatch{AckHash, AckTiming} {
		if !a.Valid() {
			t.Errorf("%s should be valid", a)
		}
	}
	if AgentState("").Valid() || MessageState("x").Valid() || Confidence("").Valid() ||
		Origin("").Valid() || TaskState("").Valid() || AckMatch("").Valid() {
		t.Error("empty/unknown enum values must be invalid")
	}
}

// --- attention ---------------------------------------------------------------

func TestAttentionEveryAgentStateWithNoMessages(t *testing.T) {
	cases := []struct {
		state   AgentState
		section Section
		label   string
	}{
		{StateStarting, SectionRunning, "starting"},
		{StateWorking, SectionRunning, "working"},
		{StateReady, SectionIdle, "ready"},
		{StateStopped, SectionIdle, "exited"},
		{StateAwaitingInput, SectionNeedsYou, "awaiting input"},
		{StateFailed, SectionNeedsYou, "failed"},
		{StateDisconnected, SectionNeedsYou, "disconnected"},
		{StateUnknown, SectionUnknown, "unknown"},
	}
	for _, c := range cases {
		got := ClassifyAttention(AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: c.state})
		if got.Section != c.section || got.Label != c.label {
			t.Errorf("%s: got %s/%q, want %s/%q", c.state, got.Section, got.Label, c.section, c.label)
		}
	}
}

// TestAttentionMatrix covers every agent state x message summary.
func TestAttentionMatrix(t *testing.T) {
	summaries := map[string]MessageSummary{
		"none":         {},
		"queued":       {Queued: 1},
		"delivering":   {Delivering: 1},
		"sent":         {Sent: 1},
		"acknowledged": {Acknowledged: 3},
		"unconfirmed":  {Unconfirmed: 1},
		"failed":       {Failed: 1},
		"cancelled":    {Cancelled: 2},
	}
	baseSection := map[AgentState]Section{
		StateStarting:      SectionRunning,
		StateWorking:       SectionRunning,
		StateReady:         SectionIdle,
		StateStopped:       SectionIdle,
		StateAwaitingInput: SectionNeedsYou,
		StateFailed:        SectionNeedsYou,
		StateDisconnected:  SectionNeedsYou,
		StateUnknown:       SectionUnknown,
	}
	for _, st := range allAgentStates {
		for name, ms := range summaries {
			want := baseSection[st]
			switch {
			case ms.Unconfirmed > 0 || ms.Failed > 0:
				want = SectionNeedsYou
			case ms.Queued > 0 && (st == StateUnknown || st.IsTerminal()):
				want = SectionNeedsYou
			}
			got := ClassifyAttention(AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: st, Messages: ms})
			if got.Section != want {
				t.Errorf("%s x %s: section %s, want %s (label %q)", st, name, got.Section, want, got.Label)
			}
			if got.Label == "" {
				t.Errorf("%s x %s: empty label", st, name)
			}
		}
	}
}

func TestAttentionLabels(t *testing.T) {
	cases := []struct {
		name string
		in   AttentionInput
		sec  Section
		lbl  string
	}{
		{"queued on unknown", AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: StateUnknown, Messages: MessageSummary{Queued: 2}}, SectionNeedsYou, "queued · attach to deliver"},
		{"unconfirmed on ready", AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: StateReady, Messages: MessageSummary{Unconfirmed: 1}}, SectionNeedsYou, "message unconfirmed · attach to check"},
		{"failed message on working", AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: StateWorking, Messages: MessageSummary{Failed: 1}}, SectionNeedsYou, "message failed"},
		{"agent state outranks messages", AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: StateAwaitingInput, Messages: MessageSummary{Unconfirmed: 1}}, SectionNeedsYou, "awaiting input"},
		{"queued while working is fine", AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: StateWorking, Messages: MessageSummary{Queued: 1}}, SectionRunning, "working"},
		{"queued on exited", AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: StateStopped, Messages: MessageSummary{Queued: 1}}, SectionNeedsYou, "queued · agent exited"},
		{"no session", AttentionInput{TaskState: TaskRunning}, SectionUnknown, "no session"},
		{"closed hides everything", AttentionInput{TaskState: TaskClosed, HasSession: true, AgentState: StateAwaitingInput}, SectionClosed, "closed"},
	}
	for _, c := range cases {
		got := ClassifyAttention(c.in)
		if got.Section != c.sec || got.Label != c.lbl {
			t.Errorf("%s: got %s/%q, want %s/%q", c.name, got.Section, got.Label, c.sec, c.lbl)
		}
	}
}

func TestUnknownNeverRendersAsSuccess(t *testing.T) {
	for _, st := range []AgentState{StateUnknown, StateFailed, StateStopped, StateDisconnected} {
		a := ClassifyAttention(AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: st})
		for _, bad := range []string{"ready", "idle", "done", "success", "ok"} {
			if strings.Contains(strings.ToLower(a.Label), bad) {
				t.Errorf("%s label %q contains %q", st, a.Label, bad)
			}
		}
	}
}

func TestSectionOrderAndTitles(t *testing.T) {
	order := []Section{SectionNeedsYou, SectionIdle, SectionRunning, SectionUnknown, SectionClosed}
	for i := 1; i < len(order); i++ {
		if order[i-1].Order() >= order[i].Order() {
			t.Errorf("%s must sort before %s", order[i-1], order[i])
		}
	}
	titles := map[Section]string{SectionNeedsYou: "NEEDS YOU", SectionIdle: "IDLE", SectionRunning: "RUNNING", SectionUnknown: "UNKNOWN", SectionClosed: "CLOSED"}
	for s, want := range titles {
		if s.Title() != want {
			t.Errorf("%s.Title() = %q, want %q", s, s.Title(), want)
		}
	}
	if len(Sections()) != 5 {
		t.Errorf("Sections() = %v", Sections())
	}
}

// --- liveness ----------------------------------------------------------------

func TestClassifyLiveness(t *testing.T) {
	id := Identity{Socket: "/tmp/tmux-1/default", ServerPID: 100, SessionID: "$1", PaneID: "%3", PanePID: 200}
	up := func(mod func(*PaneObservation)) PaneObservation {
		o := PaneObservation{ServerReachable: true, ServerPID: 100, Found: true, PanePID: 200}
		if mod != nil {
			mod(&o)
		}
		return o
	}
	cases := []struct {
		name   string
		id     Identity
		obs    PaneObservation
		status LivenessStatus
		reason DisconnectReason
		state  AgentState
		detail string
	}{
		{"match", id, up(nil), LivenessAlive, "", "", ""},
		{"pane missing", id, up(func(o *PaneObservation) { o.Found = false }), LivenessDisconnected, ReasonPaneMissing, StateDisconnected, "disconnected: pane_missing"},
		{"pane pid differs", id, up(func(o *PaneObservation) { o.PanePID = 999 }), LivenessDisconnected, ReasonIdentityMismatch, StateDisconnected, "disconnected: identity_mismatch"},
		{"server pid differs", id, up(func(o *PaneObservation) { o.ServerPID = 101 }), LivenessDisconnected, ReasonIdentityMismatch, StateDisconnected, "disconnected: identity_mismatch"},
		{"server pid differs and pane reused", id, up(func(o *PaneObservation) { o.ServerPID = 101; o.PanePID = 555 }), LivenessDisconnected, ReasonIdentityMismatch, StateDisconnected, "disconnected: identity_mismatch"},
		{"socket gone", id, PaneObservation{ServerReachable: false}, LivenessDisconnected, ReasonServerGone, StateDisconnected, "disconnected: server_gone"},
		{"dead 0", id, up(func(o *PaneObservation) { o.Dead = true; o.DeadStatus = 0 }), LivenessDead, "", StateStopped, "exit 0"},
		{"dead non-zero", id, up(func(o *PaneObservation) { o.Dead = true; o.DeadStatus = 3 }), LivenessDead, "", StateFailed, "exit 3"},
		{"dead by signal", id, up(func(o *PaneObservation) { o.Dead = true; o.DeadSignal = 9 }), LivenessDead, "", StateFailed, "signal 9"},
		{"dead but reused pid", id, up(func(o *PaneObservation) { o.Dead = true; o.PanePID = 1 }), LivenessDisconnected, ReasonIdentityMismatch, StateDisconnected, "disconnected: identity_mismatch"},
		{"no identity captured", Identity{}, up(nil), LivenessDisconnected, ReasonNoIdentity, StateDisconnected, "disconnected: no_identity"},
	}
	for _, c := range cases {
		got := ClassifyLiveness(c.id, c.obs)
		if got.Status != c.status || got.Reason != c.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, got.Status, got.Reason, c.status, c.reason)
			continue
		}
		st, detail, ok := got.ImpliedState()
		if c.state == "" {
			if ok {
				t.Errorf("%s: alive must not imply a state, got %s", c.name, st)
			}
			if !got.IsAlive() {
				t.Errorf("%s: IsAlive() = false", c.name)
			}
			continue
		}
		if !ok || st != c.state || detail != c.detail {
			t.Errorf("%s: ImpliedState() = %s %q %v, want %s %q", c.name, st, detail, ok, c.state, c.detail)
		}
		if got.IsAlive() {
			t.Errorf("%s: IsAlive() = true", c.name)
		}
	}
}

func TestUnverifiedLivenessImpliesNothing(t *testing.T) {
	l := UnverifiedLiveness("tmux: exec failed")
	if l.IsAlive() {
		t.Fatal("unverified must not be alive")
	}
	if _, _, ok := l.ImpliedState(); ok {
		t.Fatal("unverified must not imply a state change (a transient error is never sticky)")
	}
	if !strings.Contains(l.String(), "tmux: exec failed") {
		t.Fatalf("String() = %q", l.String())
	}
}

func TestIdentityIsZero(t *testing.T) {
	if !(Identity{}).IsZero() {
		t.Fatal("zero identity")
	}
	if (Identity{PaneID: "%1"}).IsZero() {
		t.Fatal("pane id set")
	}
}

// --- slug --------------------------------------------------------------------

func TestValidateSlug(t *testing.T) {
	good := []string{"a", "demo", "auth-tests", "0", "a-b-c", "x1", strings.Repeat("a", 40), "9lives", "a-"}
	for _, s := range good {
		if err := ValidateSlug(s); err != nil {
			t.Errorf("ValidateSlug(%q) = %v, want nil", s, err)
		}
	}
	bad := []string{
		"", "-lead", "has.dot", "has:colon", "has/slash", "has space", "tab\tx", "nl\nx",
		"Upper", "under_score", strings.Repeat("a", 41), "é", "a;b", "a$b", "..", ".",
	}
	for _, s := range bad {
		err := ValidateSlug(s)
		if err == nil {
			t.Errorf("ValidateSlug(%q) = nil, want error", s)
			continue
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("ValidateSlug(%q) error is multi-line: %q", s, err)
		}
	}
}

// --- events ------------------------------------------------------------------

func TestEventTypesAreDistinctAndDotted(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range EventTypes() {
		if seen[e] {
			t.Errorf("duplicate event type %q", e)
		}
		seen[e] = true
		if !strings.Contains(e, ".") {
			t.Errorf("event type %q is not dotted", e)
		}
	}
	for _, want := range []string{
		"ui.opened", "ui.closed", "ui.action", "task.created", "task.closed",
		"session.started", "session.adopted", "agent.state_changed", "session.disconnected",
		"message.queued", "message.claimed", "message.sent", "message.acknowledged",
		"message.unconfirmed", "message.cancelled", "hook.received", "hook.orphan",
	} {
		if !seen[want] {
			t.Errorf("missing event type %q", want)
		}
	}
}

func ExampleClassifyAttention() {
	a := ClassifyAttention(AttentionInput{TaskState: TaskRunning, HasSession: true, AgentState: StateStopped})
	fmt.Println(a.Section.Title(), a.Label)
	// Output: IDLE exited
}
