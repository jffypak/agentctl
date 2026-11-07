package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
)

func eventsOf(t *testing.T, st *Store, taskID string) []Event {
	t.Helper()
	evs, err := st.Events().ForTask(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestReconcileTransitionDisconnectAppendsReasonOnce(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	in := ReconcileInput{
		SessionID: sess.ID, To: domain.StateDisconnected, Confidence: domain.ConfidencePane,
		Detail: "disconnected: pane_missing", Source: domain.SourcePane, Reason: domain.ReasonPaneMissing,
	}
	res, err := st.Sessions().ReconcileTransition(ctx, in)
	if err != nil || !res.Changed || res.From != domain.StateStarting {
		t.Fatalf("first = %+v %v", res, err)
	}
	evs := eventsOf(t, st, "demo")
	if len(evs) != 2 || evs[0].Type != domain.EventSessionDisconnected || evs[1].Type != domain.EventAgentStateChanged {
		t.Fatalf("events = %+v", evs)
	}
	var disc map[string]any
	_ = json.Unmarshal(evs[0].Payload, &disc)
	if disc["reason"] != "pane_missing" || disc["from"] != "starting" || evs[0].SessionID != sess.ID {
		t.Fatalf("session.disconnected payload = %s", evs[0].Payload)
	}
	var changed map[string]any
	_ = json.Unmarshal(evs[1].Payload, &changed)
	if changed["source"] != "pane" || changed["to"] != "disconnected" || changed["reason"] != "pane_missing" || evs[1].CauseID != evs[0].ID {
		t.Fatalf("agent.state_changed = %s cause %d", evs[1].Payload, evs[1].CauseID)
	}
	got, _ := st.Sessions().Get(ctx, sess.ID)
	if got.State != domain.StateDisconnected || got.Confidence != domain.ConfidencePane || got.EndedAt.IsZero() {
		t.Fatalf("session = %+v", got)
	}

	// idempotent: a second (or concurrent) reconcile with the same verdict changes nothing
	res, err = st.Sessions().ReconcileTransition(ctx, in)
	if err != nil || res.Changed {
		t.Fatalf("second = %+v %v", res, err)
	}
	if n := len(eventsOf(t, st, "demo")); n != 2 {
		t.Fatalf("events after second pass = %d, want 2", n)
	}
	again, _ := st.Sessions().Get(ctx, sess.ID)
	if again != got {
		t.Fatalf("row changed on second pass:\n%+v\n%+v", got, again)
	}
}

func TestReconcileTransitionTerminalIsBenignNoOp(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateStopped, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	before := len(eventsOf(t, st, "demo"))
	res, err := st.Sessions().ReconcileTransition(ctx, ReconcileInput{
		SessionID: sess.ID, To: domain.StateFailed, Confidence: domain.ConfidencePane, Detail: "exit 3", Source: domain.SourcePane,
	})
	if err != nil || res.Changed || res.From != domain.StateStopped {
		t.Fatalf("terminal -> failed = %+v %v, want a benign no-op", res, err)
	}
	if got, _ := st.Sessions().Get(ctx, sess.ID); got.State != domain.StateStopped {
		t.Fatalf("state = %s", got.State)
	}
	if after := len(eventsOf(t, st, "demo")); after != before {
		t.Fatalf("events %d -> %d", before, after)
	}
	// and a matching hook arriving later cannot revive it either
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateReady, domain.ConfidenceHook, "", domain.SourceHook); !errors.Is(err, ErrTransitionNotAllowed) {
		t.Fatalf("late hook = %v, want ErrTransitionNotAllowed", err)
	}
}

func TestReconcileTransitionFromGuard(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	// a hook moved it to ready after reconcile read "starting": the timeout must not apply
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateReady, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	res, err := st.Sessions().ReconcileTransition(ctx, ReconcileInput{
		SessionID: sess.ID, From: domain.StateStarting, To: domain.StateUnknown, Confidence: domain.ConfidenceNone,
		Detail: "no hook", Source: domain.SourceTimeout,
	})
	if err != nil || res.Changed {
		t.Fatalf("guarded timeout = %+v %v", res, err)
	}
	if got, _ := st.Sessions().Get(ctx, sess.ID); got.State != domain.StateReady || got.Confidence != domain.ConfidenceHook {
		t.Fatalf("session = %+v", got)
	}
	// the guard matches: starting -> unknown with source timeout
	seedTask(t, st, "two")
	s2 := seedSession(t, st, "two", domain.StateStarting)
	res, err = st.Sessions().ReconcileTransition(ctx, ReconcileInput{
		SessionID: s2.ID, From: domain.StateStarting, To: domain.StateUnknown, Confidence: domain.ConfidenceNone,
		Detail: "no hook", Source: domain.SourceTimeout,
	})
	if err != nil || !res.Changed {
		t.Fatalf("timeout = %+v %v", res, err)
	}
	evs := eventsOf(t, st, "two")
	if len(evs) != 1 || evs[0].Type != domain.EventAgentStateChanged {
		t.Fatalf("events = %+v (no session.disconnected for a timeout)", evs)
	}
}

func TestReconcileTransitionValidates(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	if _, err := st.Sessions().ReconcileTransition(ctx, ReconcileInput{SessionID: sess.ID, To: "bogus", Confidence: domain.ConfidencePane, Source: "pane"}); err == nil {
		t.Fatal("invalid state accepted")
	}
	if _, err := st.Sessions().ReconcileTransition(ctx, ReconcileInput{SessionID: sess.ID, To: domain.StateDisconnected, Confidence: domain.ConfidencePane, Source: "pane"}); err == nil {
		t.Fatal("disconnect without a reason accepted")
	}
	if _, err := st.Sessions().ReconcileTransition(ctx, ReconcileInput{SessionID: 999, To: domain.StateStopped, Confidence: domain.ConfidencePane, Source: "pane"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session = %v", err)
	}
}

func adoptFixture(id string, ident domain.Identity) (Task, NewSession) {
	task := Task{ID: id, Name: id, WorktreePath: "/w/" + id, Branch: "feature", BaseRef: "main", BaseSHA: "abc"}
	ns := NewSession{
		TaskID: id, AgentType: "claude", Origin: domain.OriginAdopted, Identity: ident, SessionName: "work",
		State: domain.StateUnknown, Confidence: domain.ConfidenceNone,
		Events: []EventInput{{Type: domain.EventTaskCreated}, {Type: domain.EventSessionAdopted}},
	}
	return task, ns
}

func TestRegisterAdoptedIsAtomicAndRejectsDuplicates(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	repo, _ := st.Repos().Upsert(ctx, "/repos/main", "main")
	ident := domain.Identity{Socket: "/tmp/s", ServerPID: 10, SessionID: "$2", PaneID: "%4", PanePID: 44}

	task, ns := adoptFixture("foo", ident)
	task.RepoID = repo.ID
	gotTask, sess, err := st.Sessions().RegisterAdopted(ctx, task, ns)
	if err != nil {
		t.Fatal(err)
	}
	if gotTask.State != domain.TaskRunning || sess.Origin != domain.OriginAdopted || sess.State != domain.StateUnknown || sess.Identity != ident {
		t.Fatalf("task %+v session %+v", gotTask, sess)
	}
	evs := eventsOf(t, st, "foo")
	if len(evs) != 2 || evs[0].Type != domain.EventTaskCreated || evs[1].Type != domain.EventSessionAdopted || evs[1].SessionID != sess.ID {
		t.Fatalf("events = %+v", evs)
	}
	owner, err := st.Sessions().PaneOwner(ctx, ident)
	if err != nil || owner.ID != sess.ID {
		t.Fatalf("PaneOwner = %+v %v", owner, err)
	}
	onSock, err := st.Sessions().OnSocket(ctx, "/tmp/s")
	if err != nil || len(onSock) != 1 {
		t.Fatalf("OnSocket = %+v %v", onSock, err)
	}

	before := [3]int{countRows(t, st, "tasks"), countRows(t, st, "sessions"), countRows(t, st, "events")}
	// same pane identity under another name
	t2, ns2 := adoptFixture("bar", ident)
	t2.RepoID = repo.ID
	if _, _, err := st.Sessions().RegisterAdopted(ctx, t2, ns2); !errors.Is(err, ErrPaneRegistered) {
		t.Fatalf("duplicate pane = %v, want ErrPaneRegistered", err)
	}
	// same name, different pane
	t3, ns3 := adoptFixture("foo", domain.Identity{Socket: "/tmp/s", ServerPID: 10, SessionID: "$2", PaneID: "%5", PanePID: 55})
	t3.RepoID = repo.ID
	if _, _, err := st.Sessions().RegisterAdopted(ctx, t3, ns3); !errors.Is(err, ErrTaskExists) {
		t.Fatalf("duplicate name = %v, want ErrTaskExists", err)
	}
	after := [3]int{countRows(t, st, "tasks"), countRows(t, st, "sessions"), countRows(t, st, "events")}
	if before != after {
		t.Fatalf("partial rows: %v -> %v", before, after)
	}

	// a reused %N (new server pid / pane pid) is a different pane: not registered
	reused := domain.Identity{Socket: "/tmp/s", ServerPID: 11, SessionID: "$0", PaneID: "%4", PanePID: 99}
	if _, err := st.Sessions().PaneOwner(ctx, reused); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PaneOwner(reused) = %v, want ErrNotFound", err)
	}
	// identity is required
	t4, ns4 := adoptFixture("baz", domain.Identity{})
	t4.RepoID = repo.ID
	if _, _, err := st.Sessions().RegisterAdopted(ctx, t4, ns4); err == nil {
		t.Fatal("zero identity accepted")
	}
}
