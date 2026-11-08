package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
)

func TestTasksCloseLeavesSessionUntouched(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateWorking, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	beforeSess, _ := st.Sessions().Get(ctx, sess.ID)

	task, err := st.Tasks().Close(ctx, CloseInput{TaskID: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if task.State != domain.TaskClosed || task.ClosedAt.IsZero() {
		t.Fatalf("task = %+v", task)
	}
	got, _ := st.Tasks().Get(ctx, "demo")
	if got.State != domain.TaskClosed || got.ClosedAt.IsZero() || got.WorktreePath != task.WorktreePath || got.Branch != task.Branch {
		t.Fatalf("stored task = %+v", got)
	}
	if afterSess, _ := st.Sessions().Get(ctx, sess.ID); afterSess != beforeSess {
		t.Fatalf("session changed by close:\n%+v\n%+v", beforeSess, afterSess)
	}
	evs := eventsOf(t, st, "demo")
	last := evs[len(evs)-1]
	var p map[string]any
	_ = json.Unmarshal(last.Payload, &p)
	if last.Type != domain.EventTaskClosed || p["stopped"] != false {
		t.Fatalf("last event = %s %s", last.Type, last.Payload)
	}
	// closed tasks drop out of the reconcile set and the default overview
	if act, _ := st.Sessions().ListActive(ctx); len(act) != 0 {
		t.Fatalf("ListActive = %+v", act)
	}
	if rows, _ := st.Overview(ctx, OverviewFilter{}); len(rows) != 0 {
		t.Fatalf("overview = %+v", rows)
	}
	if rows, _ := st.Overview(ctx, OverviewFilter{IncludeClosed: true}); len(rows) != 1 {
		t.Fatalf("overview --closed = %+v", rows)
	}

	n := countRows(t, st, "events")
	if _, err := st.Tasks().Close(ctx, CloseInput{TaskID: "demo"}); !errors.Is(err, ErrTaskClosed) {
		t.Fatalf("second close = %v, want ErrTaskClosed", err)
	}
	if _, err := st.Tasks().Close(ctx, CloseInput{TaskID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("close missing = %v, want ErrNotFound", err)
	}
	if countRows(t, st, "events") != n {
		t.Fatal("failed closes appended events")
	}
}

func TestTasksCloseWithStopRecordsStoppedByUser(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	if _, err := st.Tasks().Close(ctx, CloseInput{TaskID: "demo", StopSessionID: sess.ID, StopDetail: "stopped by user"}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Sessions().Get(ctx, sess.ID)
	if got.State != domain.StateStopped || got.LastDetail != "stopped by user" || got.EndedAt.IsZero() {
		t.Fatalf("session = %+v", got)
	}
	evs := eventsOf(t, st, "demo")
	if len(evs) != 2 || evs[0].Type != domain.EventAgentStateChanged || evs[1].Type != domain.EventTaskClosed {
		t.Fatalf("events = %+v", evs)
	}
	var sc, tc map[string]any
	_ = json.Unmarshal(evs[0].Payload, &sc)
	_ = json.Unmarshal(evs[1].Payload, &tc)
	if sc["source"] != domain.SourceUser || tc["stopped"] != true || tc["session_id"] != float64(sess.ID) {
		t.Fatalf("payloads %s / %s", evs[0].Payload, evs[1].Payload)
	}

	// already-terminal session: close --stop keeps the recorded exit (failed stays failed)
	seedTask(t, st, "two")
	s2 := seedSession(t, st, "two", domain.StateStarting)
	if _, err := st.Sessions().Transition(ctx, s2.ID, domain.StateFailed, domain.ConfidencePane, "exit 3", domain.SourcePane); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Tasks().Close(ctx, CloseInput{TaskID: "two", StopSessionID: s2.ID, StopDetail: "stopped by user"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Sessions().Get(ctx, s2.ID); got.State != domain.StateFailed || got.LastDetail != "exit 3" {
		t.Fatalf("terminal session rewritten: %+v", got)
	}

	// a session of another task is rejected and nothing is written
	seedTask(t, st, "three")
	n := countRows(t, st, "events")
	if _, err := st.Tasks().Close(ctx, CloseInput{TaskID: "three", StopSessionID: sess.ID}); err == nil {
		t.Fatal("foreign session accepted")
	}
	if task, _ := st.Tasks().Get(ctx, "three"); task.State != domain.TaskRunning || countRows(t, st, "events") != n {
		t.Fatalf("partial close: %+v", task)
	}
}
