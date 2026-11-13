package store

import (
	"context"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
)

// TestRecordHookTextAndViewed: hook text lands on the newest session, ""
// keeps the stored value, TurnEnded stamps the turn end, and MarkViewed
// stamps the task; Overview returns all of it.
func TestRecordHookTextAndViewed(t *testing.T) {
	ctx := context.Background()
	st, _ := openTest(t)
	sess := seedSession(t, st, seedTask(t, st, "demo").ID, domain.StateStarting)

	rec := func(in HookInput) {
		t.Helper()
		in.SessionID = sess.ID
		if _, err := st.Sessions().RecordHook(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	rec(HookInput{Event: "SessionStart", State: domain.StateReady, Model: "claude-opus-5"})
	rec(HookInput{Event: "UserPromptSubmit", State: domain.StateWorking, Prompt: "fix the flaky test"})
	rec(HookInput{Event: "Stop", State: domain.StateReady, LastMessage: "Done. All tests pass.", TurnEnded: true})
	rec(HookInput{Event: "Notification"}) // "" keeps every text field

	row := overviewRow(t, st, sess.TaskID)
	want := SessionText{LastPrompt: "fix the flaky test", LastMessage: "Done. All tests pass.", Model: "claude-opus-5"}
	got := row.Text
	if got.LastPrompt != want.LastPrompt || got.LastMessage != want.LastMessage || got.Model != want.Model {
		t.Fatalf("text = %+v, want %+v", got, want)
	}
	if got.TurnEndedAt.IsZero() || !row.ViewedAt.IsZero() {
		t.Fatalf("turn ended %v, viewed %v", got.TurnEndedAt, row.ViewedAt)
	}

	if err := st.Tasks().MarkViewed(ctx, sess.TaskID); err != nil {
		t.Fatal(err)
	}
	if row := overviewRow(t, st, sess.TaskID); !row.ViewedAt.After(row.Text.TurnEndedAt) {
		t.Fatalf("viewed %v not after turn end %v", row.ViewedAt, row.Text.TurnEndedAt)
	}
	if err := st.Tasks().MarkViewed(ctx, "no-such-task"); err != ErrNotFound {
		t.Fatalf("MarkViewed(missing) = %v", err)
	}
}

func overviewRow(t *testing.T, st *Store, taskID string) OverviewRow {
	t.Helper()
	rows, err := st.Overview(context.Background(), OverviewFilter{IncludeClosed: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Task.ID == taskID {
			return r
		}
	}
	t.Fatalf("task %s not in overview", taskID)
	return OverviewRow{}
}
