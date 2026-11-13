package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// pqRig: fakeagent (no hooks) in an isolated tmux pane, registered as a
// launched session. Tests drive the session's agent state through the store
// exactly as hooks would; fakeagent's input log proves what reached it and in
// which of its own states.
type pqRig struct {
	t    *testing.T
	env  testenv.Env
	srv  *testenv.TmuxServer
	ctl  *Controller
	sess store.Session
	log  string
}

func pqTunables() Tunables {
	return Tunables{AckWindow: 2 * time.Second, DeliverSettle: time.Millisecond, PasteEnterDelay: 20 * time.Millisecond}
}

func newPQRig(t *testing.T, tun Tunables, fakeEnv ...string) *pqRig {
	t.Helper()
	fake := testenv.BuildFakeAgent(t)
	base := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, base)
	repo := testenv.TempRepo(t, srv.Env)
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "input.jsonl")
	argv := append([]string{"/usr/bin/env", "FAKEAGENT_LOG=" + log, "FAKEAGENT_WORK_MS=50"}, fakeEnv...)
	argv = append(argv, fake, "--settings", settings)
	created, err := srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{Name: "jpark-demo", Dir: repo.Root, Argv: argv})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, 0, "fakeagent prompt", func() (bool, string) {
		out, _ := srv.TryRun("capture-pane", "-p", "-t", created.PaneID)
		return strings.HasSuffix(strings.TrimSpace(out), ">"), out
	})
	ctl, err := Open(context.Background(), Config{Environ: srv.Env.Strings(), AgentctlPath: "/opt/agentctl/bin/agentctl", Tunables: tun})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctl.Close() })
	r := &pqRig{t: t, env: srv.Env, srv: srv, ctl: ctl, log: log}
	ctx := context.Background()
	rp, err := ctl.store.Repos().Upsert(ctx, repo.Root, "repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.store.Tasks().Reserve(ctx, store.Task{ID: "demo", RepoID: rp.ID, Name: "demo", WorktreePath: repo.Root, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	r.sess, err = ctl.store.Sessions().Create(ctx, store.NewSession{
		TaskID: "demo", AgentType: "claude", Origin: domain.OriginLaunched, SessionName: "jpark-demo",
		Identity: domain.Identity{Socket: srv.Socket, ServerPID: created.ServerPID, SessionID: created.SessionID, PaneID: created.PaneID, PanePID: created.PanePID},
		State:    domain.StateStarting, Confidence: domain.ConfidenceNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.hook(domain.StateReady)
	return r
}

// hook moves the session as a hook would (confidence hook).
func (r *pqRig) hook(to domain.AgentState) {
	r.t.Helper()
	if _, err := r.ctl.store.Sessions().Transition(context.Background(), r.sess.ID, to, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
		r.t.Fatal(err)
	}
}

// nextTurn is the agent working the prompt and stopping: a new ready period.
func (r *pqRig) nextTurn() {
	r.t.Helper()
	r.hook(domain.StateWorking)
	time.Sleep(2 * time.Millisecond) // state_since has millisecond resolution
	r.hook(domain.StateReady)
}

func (r *pqRig) send(body string) SendResult {
	r.t.Helper()
	res, err := r.ctl.Send(context.Background(), SendRequest{TaskID: "demo", Body: body})
	if err != nil {
		r.t.Fatalf("send %q: %v", body, err)
	}
	return res
}

type fakeInput struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	State string `json:"state_at_receipt"`
}

func readInputs(t *testing.T, path string) []fakeInput {
	t.Helper()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []fakeInput
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var in fakeInput
		if err := json.Unmarshal(sc.Bytes(), &in); err != nil {
			t.Fatal(err)
		}
		out = append(out, in)
	}
	return out
}

func (r *pqRig) pastes() []fakeInput {
	r.t.Helper()
	var out []fakeInput
	for _, in := range readInputs(r.t, r.log) {
		if in.Kind == "paste" {
			out = append(out, in)
		}
	}
	return out
}

// waitPastes waits until fakeagent logged n pastes, each followed by its submit.
func (r *pqRig) waitPastes(n int) []fakeInput {
	r.t.Helper()
	var got []fakeInput
	testenv.WaitFor(r.t, 0, strconv.Itoa(n)+" pastes + submits", func() (bool, string) {
		got = readInputs(r.t, r.log)
		subs := 0
		for _, in := range got {
			if in.Kind == "submit" {
				subs++
			}
		}
		return len(r.pastes()) >= n && subs >= n, strconv.Itoa(len(got)) + " inputs"
	})
	return r.pastes()
}

func (r *pqRig) message(id int64) store.Message {
	r.t.Helper()
	m, err := r.ctl.store.Messages().Get(context.Background(), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

func (r *pqRig) noBuffers() {
	r.t.Helper()
	bufs, err := r.srv.Client().ListBuffers(context.Background())
	if err != nil || len(bufs) != 0 {
		r.t.Fatalf("tmux buffers left: %v %v", bufs, err)
	}
}

func TestSendToReadyDeliversOnceThenWaitsForTheNextStop(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	body := "first line\nsecond line"
	res := r.send(body)
	if !res.Delivered || res.Message.State != domain.MsgSent || res.Message.Label != "sent · awaiting ack" || res.DrainErr != "" {
		t.Fatalf("send = %+v", res)
	}
	got := r.waitPastes(1)
	if got[0].Text != body || got[0].State != "ready" {
		t.Fatalf("paste = %+v, want the multi-line body received while ready", got[0])
	}
	r.noBuffers()

	second := r.send("second prompt")
	if second.Delivered || second.Message.State != domain.MsgQueued || second.Message.Label != "queued · next turn" {
		t.Fatalf("second send in the same ready period = %+v", second)
	}
	if dr, err := r.ctl.Drain(context.Background(), r.sess.ID); err != nil || dr.Delivered || dr.Reason != ReasonOnePerTurn {
		t.Fatalf("drain = %+v %v", dr, err)
	}
	r.nextTurn()
	if dr, err := r.ctl.Drain(context.Background(), r.sess.ID); err != nil || !dr.Delivered || dr.MessageID != second.Message.ID {
		t.Fatalf("drain after the next Stop = %+v %v", dr, err)
	}
	if got := r.waitPastes(2); got[1].Text != "second prompt" || got[1].State != "ready" {
		t.Fatalf("second paste = %+v", got[1])
	}
	// cause chain: claimed / sent point at the event that made the session ready
	evs, _ := r.ctl.store.Events().ForTask(context.Background(), "demo")
	for _, e := range evs {
		if (e.Type == domain.EventMessageClaimed || e.Type == domain.EventMessageSent) && e.CauseID == 0 {
			t.Errorf("%s has no cause_id", e.Type)
		}
	}
}

// TestDrainConcurrentGoroutines: 20 concurrent drains, one ready transition
// -> exactly one paste; the second message waits for the next Stop.
func TestDrainConcurrentGoroutines(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	ctx := context.Background()
	for _, b := range []string{"one", "two"} {
		if _, err := r.ctl.store.Messages().Enqueue(ctx, store.NewMessage{TaskID: "demo", SessionID: r.sess.ID, Body: b}); err != nil {
			t.Fatal(err)
		}
	}
	var delivered atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dr, err := r.ctl.Drain(ctx, r.sess.ID)
			if err != nil {
				t.Error(err)
			}
			if dr.Delivered {
				delivered.Add(1)
			}
		}()
	}
	wg.Wait()
	if delivered.Load() != 1 {
		t.Fatalf("%d deliveries, want 1", delivered.Load())
	}
	r.waitPastes(1)
	if got := r.pastes(); len(got) != 1 || got[0].Text != "one" {
		t.Fatalf("pastes = %+v", got)
	}
	r.nextTurn()
	if dr, _ := r.ctl.Drain(ctx, r.sess.ID); !dr.Delivered {
		t.Fatalf("second message after the next Stop = %+v", dr)
	}
	r.waitPastes(2)
}

func TestNotDeliverableStatesQueueWithTheirLabel(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	ctx := context.Background()
	cases := []struct {
		to    domain.AgentState
		conf  domain.Confidence
		label string
	}{
		{domain.StateWorking, domain.ConfidenceHook, "queued · agent working"},
		{domain.StateAwaitingInput, domain.ConfidenceHook, "queued · awaiting input"},
		{domain.StateReady, domain.ConfidencePane, "queued · attach to deliver"},
	}
	for _, c := range cases {
		if _, err := r.ctl.store.Sessions().Transition(ctx, r.sess.ID, c.to, c.conf, "", domain.SourceHook); err != nil {
			t.Fatal(err)
		}
		res := r.send("prompt while " + string(c.to))
		if res.Delivered || res.Message.Label != c.label {
			t.Fatalf("%s: send = %+v, want %q", c.to, res, c.label)
		}
		for range 3 {
			if err := r.ctl.DrainAll(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := r.ctl.CancelMessage(ctx, res.Message.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := readInputs(t, r.log); len(got) != 0 {
		t.Fatalf("something reached the agent: %+v", got)
	}
}

// TestUnknownSessionQueuesAttachToDeliver: an adopted pane (unknown / none)
// keeps the message queued with "attach to deliver", never pastes it across
// drain ticks, and the task shows in NEEDS YOU.
func TestUnknownSessionQueuesAttachToDeliver(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	ctx := context.Background()
	if _, err := r.ctl.store.DB().Exec(`UPDATE sessions SET agent_state = 'unknown', state_confidence = 'none', origin = 'adopted' WHERE id = ?`, r.sess.ID); err != nil {
		t.Fatal(err)
	}
	res := r.send("hello")
	if res.Message.Label != "queued · attach to deliver" {
		t.Fatalf("send = %+v", res)
	}
	for range 3 {
		if err := r.ctl.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if err := r.ctl.DrainAll(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := readInputs(t, r.log); len(got) != 0 {
		t.Fatalf("pasted into an unknown agent: %+v", got)
	}
	ov, err := r.ctl.Overview(ctx, OverviewQuery{AllRepos: true})
	if err != nil {
		t.Fatal(err)
	}
	if a := ov.Rows[0].Attention; a.Section != domain.SectionNeedsYou || a.Label != "queued · attach to deliver" {
		t.Fatalf("attention = %+v", a)
	}
}

func TestSendRejectsTerminalAgentsAndStoresNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, to := range []domain.AgentState{domain.StateStopped, domain.StateFailed, domain.StateDisconnected} {
		r := newPQRig(t, pqTunables())
		if _, err := r.ctl.store.Sessions().Transition(ctx, r.sess.ID, to, domain.ConfidencePane, "", domain.SourcePane); err != nil {
			t.Fatal(err)
		}
		_, err := r.ctl.Send(ctx, SendRequest{TaskID: "demo", Body: "hello"})
		if !errors.Is(err, ErrSendRejected) || strings.Contains(err.Error(), "\n") {
			t.Fatalf("%s: send = %v", to, err)
		}
		if n := count(t, r.ctl, "messages"); n != 0 {
			t.Fatalf("%s: %d messages stored", to, n)
		}
	}
	// the pane died since the last reconcile: the send probes, records it and refuses
	r := newPQRig(t, pqTunables())
	if err := r.srv.Client().KillVerifiedSession(ctx, r.sess.Identity); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ctl.Send(ctx, SendRequest{TaskID: "demo", Body: "hello"}); !errors.Is(err, ErrSendRejected) {
		t.Fatalf("send to a killed pane = %v", err)
	}
	if n := count(t, r.ctl, "messages"); n != 0 {
		t.Fatalf("%d messages stored", n)
	}
	for _, missing := range []string{"nope", "-x"} {
		if _, err := r.ctl.Send(ctx, SendRequest{TaskID: missing, Body: "x"}); !errors.Is(err, ErrTaskNotFound) {
			t.Fatalf("send to %q = %v", missing, err)
		}
	}
}

func TestSendValidatesTheBody(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	for _, bad := range []string{"", " \n ", strings.Repeat("x", domain.MaxMessageBytes+1), "a\x00b", "a\x1b[201~b"} {
		if _, err := r.ctl.Send(context.Background(), SendRequest{TaskID: "demo", Body: bad}); !errors.Is(err, ErrInvalidMessage) {
			t.Fatalf("send %.20q = %v", bad, err)
		}
	}
	if n := count(t, r.ctl, "messages"); n != 0 {
		t.Fatalf("%d messages stored", n)
	}
}

// TestFaultAfterClaimAndAfterPaste: a crash after the claim or after the
// paste (before MarkSent) leaves the message to the next drain, which marks
// it unconfirmed once its claim is older than AckWindow; it is never pasted
// a second time.
func TestFaultAfterClaimAndAfterPaste(t *testing.T) {
	t.Parallel()
	for _, stage := range []DrainStage{DrainAfterClaim, DrainAfterPaste} {
		t.Run(string(stage), func(t *testing.T) {
			t.Parallel()
			tun := pqTunables()
			tun.AckWindow = 300 * time.Millisecond
			r := newPQRig(t, tun)
			ctx := context.Background()
			crash := errors.New("simulated crash")
			r.ctl.InjectDrainFault(func(s DrainStage, _ int64) error {
				if s == stage {
					return crash
				}
				return nil
			})
			m, err := r.ctl.store.Messages().Enqueue(ctx, store.NewMessage{TaskID: "demo", SessionID: r.sess.ID, Body: "once"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.ctl.Drain(ctx, r.sess.ID); !errors.Is(err, crash) {
				t.Fatalf("drain = %v", err)
			}
			r.ctl.InjectDrainFault(nil)
			if got := r.message(m.ID).State; got != domain.MsgDelivering {
				t.Fatalf("after the crash = %s", got)
			}
			wantPastes := 0
			if stage == DrainAfterPaste {
				wantPastes = 1
				r.waitPastes(1)
			}
			testenv.WaitFor(t, 0, "next drain marks it unconfirmed", func() (bool, string) {
				if _, err := r.ctl.Drain(ctx, r.sess.ID); err != nil {
					return false, err.Error()
				}
				st := r.message(m.ID).State
				return st == domain.MsgUnconfirmed, string(st)
			})
			r.nextTurn()
			for range 3 {
				if _, err := r.ctl.Drain(ctx, r.sess.ID); err != nil {
					t.Fatal(err)
				}
			}
			if got := r.pastes(); len(got) != wantPastes {
				t.Fatalf("%d pastes, want %d (never a second paste)", len(got), wantPastes)
			}
			if got := r.message(m.ID).State; got != domain.MsgUnconfirmed {
				t.Fatalf("final state = %s", got)
			}
			evs, _ := r.ctl.store.Events().ForTask(ctx, "demo")
			for _, e := range evs {
				if e.Type == domain.EventMessageUnconfirmed && e.CauseID == 0 {
					t.Error("message.unconfirmed has no cause_id")
				}
			}
		})
	}
}

// TestStateFlipBetweenClaimAndPasteReverts: the session goes working right
// after the claim -> re-verification reverts to queued; nothing is pasted.
func TestStateFlipBetweenClaimAndPasteReverts(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	ctx := context.Background()
	r.ctl.InjectDrainFault(func(s DrainStage, _ int64) error {
		if s == DrainAfterClaim {
			r.hook(domain.StateWorking)
		}
		return nil
	})
	m, _ := r.ctl.store.Messages().Enqueue(ctx, store.NewMessage{TaskID: "demo", SessionID: r.sess.ID, Body: "x"})
	dr, err := r.ctl.Drain(ctx, r.sess.ID)
	if err != nil || dr.Delivered || dr.Reason != domain.LabelWorking {
		t.Fatalf("drain = %+v %v", dr, err)
	}
	if got := r.message(m.ID).State; got != domain.MsgQueued {
		t.Fatalf("state = %s, want queued", got)
	}
	r.ctl.InjectDrainFault(nil)
	if got := readInputs(t, r.log); len(got) != 0 {
		t.Fatalf("pasted: %+v", got)
	}
	r.noBuffers()
	r.hook(domain.StateReady)
	if dr, _ := r.ctl.Drain(ctx, r.sess.ID); !dr.Delivered {
		t.Fatalf("after ready again = %+v", dr)
	}
	r.waitPastes(1)
}

// TestPaneIdentityMismatchNeverPastes: a stored identity that no longer
// matches the live pane (pane pid differs: %N reused) is refused by Send
// (nothing stored) and by Drain (reverted, nothing pasted). The same guard
// runs again inside tmux (tmux.TestPasteRefusedForAnotherIdentityOrCopyMode).
func TestPaneIdentityMismatchNeverPastes(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	ctx := context.Background()
	m, _ := r.ctl.store.Messages().Enqueue(ctx, store.NewMessage{TaskID: "demo", SessionID: r.sess.ID, Body: "x"})
	if _, err := r.ctl.store.DB().Exec(`UPDATE sessions SET pane_pid = pane_pid + 100000 WHERE id = ?`, r.sess.ID); err != nil {
		t.Fatal(err)
	}
	dr, err := r.ctl.Drain(ctx, r.sess.ID)
	if err != nil || dr.Delivered || dr.Reason != domain.LabelDisconnected {
		t.Fatalf("drain = %+v %v", dr, err)
	}
	if got := r.message(m.ID).State; got != domain.MsgQueued {
		t.Fatalf("message = %s", got)
	}
	if _, err := r.ctl.Send(ctx, SendRequest{TaskID: "demo", Body: "y"}); !errors.Is(err, ErrSendRejected) {
		t.Fatalf("send = %v", err)
	}
	if got := readInputs(t, r.log); len(got) != 0 {
		t.Fatalf("pasted: %+v", got)
	}
}

func TestDeliverAfterHookTimesOutToUnconfirmedAndNeverRepastes(t *testing.T) {
	t.Parallel()
	tun := pqTunables()
	tun.AckWindow = 300 * time.Millisecond
	r := newPQRig(t, tun)
	ctx := context.Background()
	m, _ := r.ctl.store.Messages().Enqueue(ctx, store.NewMessage{TaskID: "demo", SessionID: r.sess.ID, Body: "no hooks, no ack"})
	var log strings.Builder
	rep := r.ctl.DeliverAfterHook(ctx, DeliverRequest{SessionID: r.sess.ID, Log: &log})
	if !rep.Drain.Delivered || !rep.Unconfirmed || rep.Err != nil {
		t.Fatalf("report = %+v\n%s", rep, log.String())
	}
	if !strings.Contains(log.String(), "unconfirmed") {
		t.Fatalf("log = %s", log.String())
	}
	if got := r.message(m.ID); got.State != domain.MsgUnconfirmed {
		t.Fatalf("message = %+v", got)
	}
	ov, _ := r.ctl.Overview(ctx, OverviewQuery{AllRepos: true})
	if a := ov.Rows[0].Attention; a.Section != domain.SectionNeedsYou {
		t.Fatalf("attention = %+v", a)
	}
	for range 3 {
		r.nextTurn()
		if rep := r.ctl.DeliverAfterHook(ctx, DeliverRequest{SessionID: r.sess.ID}); rep.Drain.Delivered {
			t.Fatalf("re-pasted: %+v", rep)
		}
	}
	if n := len(r.pastes()); n != 1 {
		t.Fatalf("%d pastes", n)
	}
}

// TestDeliverAcksFromASubmitThatRacedThePaste: the UserPromptSubmit hook was
// recorded while the message was still delivering; deliver matches it.
func TestDeliverAcksFromASubmitThatRacedThePaste(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	ctx := context.Background()
	body := "hash me"
	r.ctl.InjectDrainFault(func(s DrainStage, _ int64) error {
		if s == DrainAfterPaste { // the submit hook lands before MarkSent
			_, err := r.ctl.store.Sessions().RecordHook(ctx, store.HookInput{SessionID: r.sess.ID, Event: "UserPromptSubmit",
				State: domain.StateWorking, Payload: map[string]any{"prompt_sha256": store.BodyHash(body)}})
			return err
		}
		return nil
	})
	m, _ := r.ctl.store.Messages().Enqueue(ctx, store.NewMessage{TaskID: "demo", SessionID: r.sess.ID, Body: body})
	rep := r.ctl.DeliverAfterHook(ctx, DeliverRequest{SessionID: r.sess.ID})
	if !rep.Acknowledged || rep.AckMatch != domain.AckHash {
		t.Fatalf("report = %+v", rep)
	}
	if got := r.message(m.ID); got.State != domain.MsgAcknowledged {
		t.Fatalf("message = %+v", got)
	}
}

func TestLostHookMarkerBlocksDeliveryAndReconcileDowngrades(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	ctx := context.Background()
	time.Sleep(2 * time.Millisecond)
	dir := LostHookDir(r.ctl.Paths().Hooks)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, domain.LostHookTaskKey("demo"))
	if err := os.WriteFile(marker, []byte(`{"ts_ms":`+strconv.FormatInt(time.Now().UnixMilli(), 10)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res := r.send("must wait")
	if res.Delivered || res.Message.Label != "queued · "+domain.LabelStale {
		t.Fatalf("send = %+v", res)
	}
	if err := r.ctl.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	sess, _ := r.ctl.store.Sessions().Get(ctx, r.sess.ID)
	if sess.State != domain.StateUnknown || sess.Confidence != domain.ConfidenceNone {
		t.Fatalf("after reconcile = %s / %s", sess.State, sess.Confidence)
	}
	if err := r.ctl.DrainAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readInputs(t, r.log); len(got) != 0 {
		t.Fatalf("pasted while stale: %+v", got)
	}
	// the next recorded hook (hookingest clears the marker) moves it on
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	r.hook(domain.StateReady)
	if dr, _ := r.ctl.Drain(ctx, r.sess.ID); !dr.Delivered {
		t.Fatalf("after the next hook = %+v", dr)
	}
	r.waitPastes(1)
}

func TestCancelQueuedOnlyAndMessagesView(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	ctx := context.Background()
	r.hook(domain.StateWorking)
	q := r.send("line one\nline two")
	if _, err := r.ctl.CancelMessage(ctx, q.Message.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ctl.CancelMessage(ctx, q.Message.ID); !errors.Is(err, ErrCancelRefused) {
		t.Fatalf("cancel twice = %v", err)
	}
	if _, err := r.ctl.CancelMessage(ctx, 999); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("cancel missing = %v", err)
	}
	r.hook(domain.StateReady)
	s := r.send("sent one")
	if _, err := r.ctl.CancelMessage(ctx, s.Message.ID); !errors.Is(err, ErrCancelRefused) {
		t.Fatalf("cancel sent = %v", err)
	}
	views, err := r.ctl.Messages(ctx, "demo")
	if err != nil || len(views) != 2 {
		t.Fatalf("views = %+v %v", views, err)
	}
	if views[0].Label != "cancelled" || views[0].Preview != "line one …" || views[0].Lines != 2 || views[1].Label != "sent · awaiting ack" {
		t.Fatalf("views = %+v", views)
	}
}
