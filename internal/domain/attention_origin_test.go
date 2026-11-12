package domain

import "testing"

// A launched agent only reaches unknown by never sending a hook (starting ->
// unknown on StartingTimeout): Claude's folder-trust prompt, a login prompt, or
// an early hang. That needs the user. An adopted pane that has sent hooks but
// whose state was lost stays muted in UNKNOWN (with no hook ever it is
// INITIALSTATE: attention_initial_test.go).
func TestAttentionUnknownDependsOnOrigin(t *testing.T) {
	cases := []struct {
		origin  Origin
		section Section
		label   string
	}{
		{OriginLaunched, SectionNeedsYou, "silent since launch · attach to check"},
		{OriginAdopted, SectionUnknown, "unknown"},
		{"", SectionUnknown, "unknown"},
	}
	for _, c := range cases {
		got := ClassifyAttention(AttentionInput{
			TaskState: TaskRunning, HasSession: true, AgentState: StateUnknown,
			Confidence: ConfidenceNone, Origin: c.origin, HasHook: true,
		})
		if got.Section != c.section || got.Label != c.label {
			t.Errorf("origin %q: got %+v, want {%s %q}", c.origin, got, c.section, c.label)
		}
	}
}

// A queued message on a silent launched agent keeps the more specific queued label.
func TestAttentionQueuedOnSilentLaunchedAgent(t *testing.T) {
	got := ClassifyAttention(AttentionInput{
		TaskState: TaskRunning, HasSession: true, AgentState: StateUnknown,
		Confidence: ConfidenceNone, Origin: OriginLaunched,
		Messages: MessageSummary{}.Add(MsgQueued, 1),
	})
	if got.Section != SectionNeedsYou || got.Label != "queued · attach to deliver" {
		t.Fatalf("got %+v", got)
	}
}
