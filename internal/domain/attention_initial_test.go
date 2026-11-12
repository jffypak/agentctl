package domain

import (
	"strings"
	"testing"
)

// INITIALSTATE holds every agent that has not sent its first hook event yet,
// except a launched (or wrapped) agent still silent after StartingTimeout,
// which stays in NEEDS YOU (user decision, codex-support design).
func TestClassifyAttentionInitialState(t *testing.T) {
	cases := []struct {
		state   AgentState
		origin  Origin
		hasHook bool
		queued  int
		section Section
		label   string
	}{
		{StateStarting, OriginLaunched, false, 0, SectionInitial, "no events yet"},
		{StateStarting, OriginWrapped, false, 0, SectionInitial, "no events yet"},
		{StateStarting, OriginLaunched, true, 0, SectionRunning, "starting"},
		{StateStarting, OriginLaunched, false, 1, SectionNeedsYou, "queued · attach to deliver"},
		{StateUnknown, OriginLaunched, false, 0, SectionNeedsYou, "silent since launch · attach to check"},
		{StateUnknown, OriginWrapped, false, 0, SectionNeedsYou, "silent since launch · attach to check"},
		{StateUnknown, OriginAdopted, false, 0, SectionInitial, "no events yet"},
		{StateUnknown, OriginAdopted, true, 0, SectionUnknown, "unknown"},
		{StateWorking, OriginWrapped, true, 0, SectionRunning, "working"},
	}
	for _, c := range cases {
		in := AttentionInput{
			TaskState: TaskRunning, HasSession: true, AgentState: c.state,
			Confidence: ConfidenceNone, Origin: c.origin, HasHook: c.hasHook,
		}
		if c.queued > 0 {
			in.Messages = MessageSummary{}.Add(MsgQueued, c.queued)
		}
		got := ClassifyAttention(in)
		if got.Section != c.section || got.Label != c.label {
			t.Errorf("%s/%s hasHook=%v queued=%d: got %+v, want {%s %q}",
				c.state, c.origin, c.hasHook, c.queued, got, c.section, c.label)
		}
	}
}

func TestSectionsOrder(t *testing.T) {
	want := []Section{SectionNeedsYou, SectionIdle, SectionRunning, SectionInitial, SectionUnknown, SectionClosed}
	got := Sections()
	if len(got) != len(want) {
		t.Fatalf("Sections() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Sections() = %v, want %v", got, want)
		}
		if got[i].Order() != i {
			t.Errorf("%s.Order() = %d, want %d", got[i], got[i].Order(), i)
		}
	}
	if SectionInitial.Title() != "INITIALSTATE" {
		t.Errorf("SectionInitial.Title() = %q, want INITIALSTATE", SectionInitial.Title())
	}
}

func TestOriginWrappedValid(t *testing.T) {
	if !OriginWrapped.Valid() || OriginWrapped != "wrapped" {
		t.Fatalf("OriginWrapped = %q, Valid %v", OriginWrapped, OriginWrapped.Valid())
	}
}

func TestShowsCodexHookTrustDialog(t *testing.T) {
	yes := []string{
		"Hooks need review\n  1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting (hooks won't run)",
		"│ 2. TRUST ALL AND CONTINUE │",
		"3. Continue without\n   trusting (hooks won't run)",
	}
	for _, s := range yes {
		if !ShowsCodexHookTrustDialog(s) {
			t.Errorf("ShowsCodexHookTrustDialog(%q) = false, want true", s)
		}
	}
	no := []string{
		"",
		"trust",
		"Do you trust the files in this folder?",
		"Quick safety check: Is this a project you created or one you trust?",
		"› Ask Codex to do anything",
	}
	for _, s := range no {
		if ShowsCodexHookTrustDialog(s) {
			t.Errorf("ShowsCodexHookTrustDialog(%q) = true, want false", s)
		}
	}
	if HookTrustDetail != "hook trust: attach to answer" {
		t.Errorf("HookTrustDetail = %q", HookTrustDetail)
	}
	if DetailAwaitingFirstPrompt != "waiting for first prompt · attach to start" {
		t.Errorf("DetailAwaitingFirstPrompt = %q", DetailAwaitingFirstPrompt)
	}
}

func TestSlugFrom(t *testing.T) {
	cases := map[string]string{
		"arize":                        "arize",
		"My Repo!":                     "my-repo",
		"---":                          "",
		"":                             "",
		"a__b..c":                      "a-b-c",
		strings.Repeat("a", 60):        strings.Repeat("a", 40),
		"x" + strings.Repeat("-y", 30): "x" + strings.Repeat("-y", 19) + "",
	}
	for in, want := range cases {
		got := SlugFrom(in)
		if got != want {
			t.Errorf("SlugFrom(%q) = %q, want %q", in, got, want)
		}
		if got != "" && ValidateSlug(got) != nil {
			t.Errorf("SlugFrom(%q) = %q fails ValidateSlug: %v", in, got, ValidateSlug(got))
		}
	}
}
