package domain

import (
	"strings"
	"testing"
	"time"
)

// TestCanDeliverTable: only ready + hook confidence + alive (and not stale)
// passes; every other combination returns its label.
func TestCanDeliverTable(t *testing.T) {
	livenesses := []LivenessStatus{LivenessAlive, LivenessDead, LivenessDisconnected, LivenessUnverified, ""}
	confs := []Confidence{ConfidenceHook, ConfidencePane, ConfidenceNone}
	for _, st := range AgentStates() {
		for _, conf := range confs {
			for _, l := range livenesses {
				for _, stale := range []bool{false, true} {
					ok, label := CanDeliver(DeliveryCheck{State: st, Confidence: conf, Liveness: l, StaleHook: stale})
					want := st == StateReady && conf == ConfidenceHook && l == LivenessAlive && !stale
					if ok != want {
						t.Errorf("CanDeliver(%s, %s, %q, stale=%v) = %v, want %v", st, conf, l, stale, ok, want)
					}
					if ok != (label == "") {
						t.Errorf("CanDeliver(%s, %s, %q, stale=%v): ok=%v label=%q", st, conf, l, stale, ok, label)
					}
				}
			}
		}
	}
	cases := []struct {
		in   DeliveryCheck
		want string
	}{
		{DeliveryCheck{StateWorking, ConfidenceHook, LivenessAlive, false}, LabelWorking},
		{DeliveryCheck{StateAwaitingInput, ConfidenceHook, LivenessAlive, false}, LabelAwaiting},
		{DeliveryCheck{StateUnknown, ConfidenceNone, LivenessAlive, false}, LabelAttach},
		{DeliveryCheck{StateReady, ConfidencePane, LivenessAlive, false}, LabelAttach},
		{DeliveryCheck{StateReady, ConfidenceNone, LivenessAlive, false}, LabelAttach},
		{DeliveryCheck{StateDisconnected, ConfidencePane, LivenessDisconnected, false}, LabelDisconnected},
		{DeliveryCheck{StateReady, ConfidenceHook, LivenessDisconnected, false}, LabelDisconnected},
		{DeliveryCheck{StateReady, ConfidenceHook, LivenessDead, false}, LabelExited},
		{DeliveryCheck{StateStopped, ConfidencePane, LivenessDead, false}, LabelExited},
		{DeliveryCheck{StateFailed, ConfidencePane, LivenessDead, false}, LabelFailed},
		{DeliveryCheck{StateStarting, ConfidenceNone, LivenessAlive, false}, LabelStarting},
		{DeliveryCheck{StateReady, ConfidenceHook, LivenessUnverified, false}, LabelUnverified},
		{DeliveryCheck{StateReady, ConfidenceHook, "", false}, LabelUnverified},
		{DeliveryCheck{StateReady, ConfidenceHook, LivenessAlive, true}, LabelStale},
		{DeliveryCheck{StateWorking, ConfidenceHook, LivenessAlive, true}, LabelStale},
	}
	for _, c := range cases {
		if ok, label := CanDeliver(c.in); ok || label != c.want {
			t.Errorf("CanDeliver(%+v) = %v %q, want false %q", c.in, ok, label, c.want)
		}
	}
	if ok, _ := CanDeliver(DeliveryCheck{StateReady, ConfidenceHook, LivenessAlive, false}); !ok {
		t.Error("ready + hook + alive must be deliverable")
	}
}

func TestCanDeliverNeverIntoAwaitingInput(t *testing.T) {
	for _, conf := range []Confidence{ConfidenceHook, ConfidencePane, ConfidenceNone} {
		if ok, _ := CanDeliver(DeliveryCheck{StateAwaitingInput, conf, LivenessAlive, false}); ok {
			t.Fatalf("awaiting_input (%s) must never be deliverable", conf)
		}
	}
}

func TestQueueLabel(t *testing.T) {
	if got := QueueLabel(StateReady, ConfidenceHook, false); got != "" {
		t.Errorf("ready/hook = %q", got)
	}
	if got := QueueLabel(StateWorking, ConfidenceHook, false); got != LabelWorking {
		t.Errorf("working = %q", got)
	}
	if got := QueueLabel(StateReady, ConfidenceHook, true); got != LabelStale {
		t.Errorf("stale = %q", got)
	}
}

func TestSendRejected(t *testing.T) {
	for _, s := range AgentStates() {
		want := s == StateStopped || s == StateFailed || s == StateDisconnected
		if got := SendRejected(s); got != want {
			t.Errorf("SendRejected(%s) = %v", s, got)
		}
	}
}

func TestValidateMessageBody(t *testing.T) {
	ok := []string{"x", "run the tests", "line one\nline two\n\tindented", strings.Repeat("a", MaxMessageBytes), "héllo ✓"}
	for _, b := range ok {
		if err := ValidateMessageBody(b); err != nil {
			t.Errorf("ValidateMessageBody(%.20q) = %v", b, err)
		}
	}
	bad := map[string]string{
		"":                                     "empty",
		"  \n\t ":                              "empty",
		strings.Repeat("a", MaxMessageBytes+1): "bytes",
		"a\x00b":                               "NUL",
		"a\x1b[201~b":                          "escape",
		"\xff\xfe":                             "UTF-8",
	}
	for b, want := range bad {
		err := ValidateMessageBody(b)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateMessageBody(%.20q) = %v, want %q", b, err, want)
		}
	}
}

func TestLostHookKeys(t *testing.T) {
	got := strings.Join(LostHookKeys(7, "demo", "%3"), " ")
	if got != "session-7 task-demo pane-3" {
		t.Fatalf("keys = %q", got)
	}
	if got := LostHookKeys(7, "../etc", "3;rm"); len(got) != 1 || got[0] != "session-7" {
		t.Fatalf("unsafe ids must not become keys: %q", got)
	}
	if LostHookTaskKey("") != "" || LostHookPaneKey("%") != "" || LostHookPaneKey("%-1") != "" {
		t.Fatal("empty or malformed ids must not become keys")
	}
}

func TestMatchAck(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	w := 15 * time.Second
	sent := func(id int64, hash string, at time.Time) AckCandidate {
		return AckCandidate{ID: id, State: MsgSent, BodySHA256: hash, ClaimedAt: at.Add(-50 * time.Millisecond), SentAt: at}
	}
	cases := []struct {
		name string
		in   AckInput
		want AckDecision
	}{
		{"hash", AckInput{"h1", t0.Add(time.Second), w, []AckCandidate{sent(1, "h1", t0)}}, AckDecision{1, AckHash}},
		{"hash wins over an older timing match", AckInput{"h2", t0.Add(time.Second), w,
			[]AckCandidate{sent(1, "h1", t0), sent(2, "h2", t0)}}, AckDecision{2, AckHash}},
		{"late hash on unconfirmed", AckInput{"h1", t0.Add(time.Minute), w,
			[]AckCandidate{{ID: 1, State: MsgUnconfirmed, BodySHA256: "h1", SentAt: t0}}}, AckDecision{1, AckHash}},
		{"timing (placeholder)", AckInput{"other", t0.Add(2 * time.Second), w, []AckCandidate{sent(1, "h1", t0)}}, AckDecision{1, AckTiming}},
		{"timing at the window edge", AckInput{"", t0.Add(w), w, []AckCandidate{sent(1, "h1", t0)}}, AckDecision{1, AckTiming}},
		{"timing past the window", AckInput{"", t0.Add(w + time.Millisecond), w, []AckCandidate{sent(1, "h1", t0)}}, AckDecision{}},
		{"submit raced MarkSent (after claim)", AckInput{"", t0.Add(-10 * time.Millisecond), w, []AckCandidate{sent(1, "h1", t0)}}, AckDecision{1, AckTiming}},
		{"submit before the claim", AckInput{"", t0.Add(-time.Second), w, []AckCandidate{sent(1, "h1", t0)}}, AckDecision{}},
		{"unconfirmed never by timing", AckInput{"", t0.Add(time.Second), w,
			[]AckCandidate{{ID: 1, State: MsgUnconfirmed, BodySHA256: "h1", SentAt: t0}}}, AckDecision{}},
		{"oldest sent first", AckInput{"", t0.Add(time.Second), w, []AckCandidate{sent(3, "c", t0), sent(2, "b", t0)}}, AckDecision{2, AckTiming}},
		{"delivering and acknowledged never match", AckInput{"h1", t0.Add(time.Second), w, []AckCandidate{
			{ID: 1, State: MsgDelivering, BodySHA256: "h1", SentAt: t0},
			{ID: 2, State: MsgAcknowledged, BodySHA256: "h1", SentAt: t0}}}, AckDecision{}},
		{"nothing pending", AckInput{"h1", t0, w, nil}, AckDecision{}},
	}
	for _, c := range cases {
		if got := MatchAck(c.in); got != c.want {
			t.Errorf("%s: MatchAck = %+v, want %+v", c.name, got, c.want)
		}
	}
}
