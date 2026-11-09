package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
)

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// readySession seeds task id with a session moved to ready by a hook.
func readySession(t *testing.T, st *Store, id string) Session {
	t.Helper()
	seedTask(t, st, id)
	sess := seedSession(t, st, id, domain.StateStarting)
	if _, err := st.Sessions().Transition(context.Background(), sess.ID, domain.StateReady, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	got, err := st.Sessions().Get(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func enqueue(t *testing.T, st *Store, sess Session, body string) Message {
	t.Helper()
	m, err := st.Messages().Enqueue(context.Background(), NewMessage{TaskID: sess.TaskID, SessionID: sess.ID, Body: body})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	return m
}

func msgEvents(t *testing.T, st *Store, typ string) []Event {
	t.Helper()
	all, err := st.Events().Since(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range all {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func TestEnqueueStoresHashAndAppendsQueued(t *testing.T) {
	st, _ := openTest(t)
	sess := readySession(t, st, "demo")
	m := enqueue(t, st, sess, "run the tests\nplease")
	if m.State != domain.MsgQueued || m.BodySHA256 != sha("run the tests\nplease") || m.Body != "run the tests\nplease" {
		t.Fatalf("message = %+v", m)
	}
	evs := msgEvents(t, st, domain.EventMessageQueued)
	if len(evs) != 1 || evs[0].TaskID != "demo" || evs[0].SessionID != sess.ID {
		t.Fatalf("queued events = %+v", evs)
	}
	var p map[string]any
	_ = json.Unmarshal(evs[0].Payload, &p)
	if p["message_id"] != float64(m.ID) || p["body"] != nil {
		t.Fatalf("payload = %v (must carry the id and never the body)", p)
	}
	if _, err := st.Messages().Enqueue(context.Background(), NewMessage{TaskID: "demo", SessionID: sess.ID, Body: "a\x00b"}); err == nil {
		t.Fatal("NUL body accepted")
	}
	if _, err := st.Messages().Enqueue(context.Background(), NewMessage{TaskID: "other", SessionID: sess.ID, Body: "x"}); err == nil {
		t.Fatal("message for a session of another task accepted")
	}
}

func TestClaimOnePerReadyTransition(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	sess := readySession(t, st, "demo")
	m1, m2 := enqueue(t, st, sess, "one"), enqueue(t, st, sess, "two")

	c, ok, err := st.Messages().Claim(ctx, sess.ID, 77)
	if err != nil || !ok || c.Message.ID != m1.ID || c.Message.State != domain.MsgDelivering || !c.StateSince.Equal(sess.StateSince) {
		t.Fatalf("claim = %+v %v %v", c, ok, err)
	}
	if _, ok, _ := st.Messages().Claim(ctx, sess.ID, 0); ok {
		t.Fatal("a second claim while one is delivering")
	}
	if _, err := st.Messages().MarkSent(ctx, m1.ID, st.Now(), 77); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Messages().Claim(ctx, sess.ID, 0); ok {
		t.Fatal("a second claim in the same ready period")
	}
	// the agent works the prompt and finishes: a new ready transition
	for _, s := range []domain.AgentState{domain.StateWorking, domain.StateReady} {
		if _, err := st.Sessions().Transition(ctx, sess.ID, s, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
			t.Fatal(err)
		}
	}
	c, ok, err = st.Messages().Claim(ctx, sess.ID, 0)
	if err != nil || !ok || c.Message.ID != m2.ID {
		t.Fatalf("claim after the next Stop = %+v %v %v", c, ok, err)
	}
	claimed := msgEvents(t, st, domain.EventMessageClaimed)
	if len(claimed) != 2 || claimed[0].CauseID != 77 {
		t.Fatalf("claimed events = %+v", claimed)
	}
}

func TestClaimRequiresReadyHook(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateUnknown)
	enqueue(t, st, sess, "x")
	if _, ok, err := st.Messages().Claim(ctx, sess.ID, 0); ok || err != nil {
		t.Fatalf("unknown session claimed: %v %v", ok, err)
	}
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateReady, domain.ConfidencePane, "", domain.SourcePane); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Messages().Claim(ctx, sess.ID, 0); ok {
		t.Fatal("ready without hook confidence claimed")
	}
	for _, s := range []domain.AgentState{domain.StateWorking, domain.StateAwaitingInput} {
		if _, err := st.Sessions().Transition(ctx, sess.ID, s, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := st.Messages().Claim(ctx, sess.ID, 0); ok {
			t.Fatalf("%s session claimed", s)
		}
	}
}

// TestClaimConcurrentGoroutinesAndStores: 20 claimers on two Store handles,
// one ready transition -> exactly one claim.
func TestClaimConcurrentGoroutinesAndStores(t *testing.T) {
	st, path := openTest(t)
	sess := readySession(t, st, "demo")
	enqueue(t, st, sess, "one")
	enqueue(t, st, sess, "two")
	st2, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := st
			if i%2 == 1 {
				s = st2
			}
			if _, ok, err := s.Messages().Claim(context.Background(), sess.ID, 0); err != nil {
				t.Error(err)
			} else if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims, want 1", wins.Load())
	}
}

func TestMessageLifecycleAndConditionalWrites(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	sess := readySession(t, st, "demo")
	m := enqueue(t, st, sess, "hello")
	ms := st.Messages()
	if _, err := ms.MarkSent(ctx, m.ID, st.Now(), 0); !errors.Is(err, ErrMessageState) {
		t.Fatalf("queued -> sent = %v", err)
	}
	if _, ok, _ := ms.Claim(ctx, sess.ID, 0); !ok {
		t.Fatal("claim")
	}
	if err := ms.Revert(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := ms.Get(ctx, m.ID); got.State != domain.MsgQueued {
		t.Fatalf("reverted = %s", got.State)
	}
	if _, ok, _ := ms.Claim(ctx, sess.ID, 0); !ok {
		t.Fatal("a reverted message is claimable again in the same ready period")
	}
	sent, err := ms.MarkSent(ctx, m.ID, st.Now(), 5)
	if err != nil || sent.State != domain.MsgSent || sent.SentAt.IsZero() {
		t.Fatalf("sent = %+v %v", sent, err)
	}
	if err := ms.Revert(ctx, m.ID); !errors.Is(err, ErrMessageState) {
		t.Fatalf("sent -> queued = %v", err)
	}
	if _, err := ms.Cancel(ctx, m.ID); !errors.Is(err, ErrMessageState) {
		t.Fatalf("cancel sent = %v", err)
	}
	ok, err := ms.MarkUnconfirmed(ctx, m.ID, "no ack", 6)
	if err != nil || !ok {
		t.Fatalf("unconfirmed = %v %v", ok, err)
	}
	if ok, _ := ms.MarkUnconfirmed(ctx, m.ID, "again", 6); ok {
		t.Fatal("unconfirmed twice")
	}
	ok, err = ms.Acknowledge(ctx, m.ID, domain.AckHash, 9)
	if err != nil || !ok {
		t.Fatalf("late hash ack = %v %v", ok, err)
	}
	got, _ := ms.Get(ctx, m.ID)
	if got.State != domain.MsgAcknowledged || got.AckMatch != domain.AckHash || got.AckedAt.IsZero() {
		t.Fatalf("acked = %+v", got)
	}
	if ok, _ := ms.Acknowledge(ctx, m.ID, domain.AckHash, 9); ok {
		t.Fatal("acknowledged twice")
	}
	for typ, cause := range map[string]int64{domain.EventMessageSent: 5, domain.EventMessageUnconfirmed: 6, domain.EventMessageAcknowledged: 9} {
		evs := msgEvents(t, st, typ)
		if len(evs) != 1 || evs[0].CauseID != cause {
			t.Errorf("%s events = %+v", typ, evs)
		}
	}
}

func TestCancelQueuedOnly(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	sess := readySession(t, st, "demo")
	m := enqueue(t, st, sess, "x")
	c, err := st.Messages().Cancel(ctx, m.ID)
	if err != nil || c.State != domain.MsgCancelled {
		t.Fatalf("cancel = %+v %v", c, err)
	}
	if _, err := st.Messages().Cancel(ctx, m.ID); !errors.Is(err, ErrMessageState) {
		t.Fatalf("cancel twice = %v", err)
	}
	if _, err := st.Messages().Cancel(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel missing = %v", err)
	}
	if n := len(msgEvents(t, st, domain.EventMessageCancelled)); n != 1 {
		t.Fatalf("%d cancelled events", n)
	}
	if _, ok, _ := st.Messages().Claim(ctx, sess.ID, 0); ok {
		t.Fatal("a cancelled message was claimed")
	}
}

func TestSweepStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentctl.db")
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	st, err := Open(context.Background(), path, Options{Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a := readySession(t, st, "a")
	b := readySession(t, st, "b")
	ma, mb := enqueue(t, st, a, "a"), enqueue(t, st, b, "b")
	if _, ok, _ := st.Messages().Claim(ctx, a.ID, 0); !ok {
		t.Fatal("claim a")
	}
	if _, ok, _ := st.Messages().Claim(ctx, b.ID, 0); !ok {
		t.Fatal("claim b")
	}
	if _, err := st.Messages().MarkSent(ctx, mb.ID, clock(), 0); err != nil {
		t.Fatal(err)
	}
	w := 15 * time.Second
	if ids, _ := st.Messages().SweepStale(ctx, 0, w); len(ids) != 0 {
		t.Fatalf("fresh messages swept: %v", ids)
	}
	advance(w + time.Millisecond)
	ids, err := st.Messages().SweepStale(ctx, a.ID, w)
	if err != nil || len(ids) != 1 || ids[0] != ma.ID {
		t.Fatalf("sweep a = %v %v", ids, err)
	}
	ids, _ = st.Messages().SweepStale(ctx, 0, w)
	if len(ids) != 1 || ids[0] != mb.ID {
		t.Fatalf("sweep all = %v", ids)
	}
	for _, id := range []int64{ma.ID, mb.ID} {
		if m, _ := st.Messages().Get(ctx, id); m.State != domain.MsgUnconfirmed || m.Error == "" {
			t.Fatalf("swept = %+v", m)
		}
	}
	// an unconfirmed message is never re-claimed, and it does not block the
	// next ready transition
	enqueue(t, st, b, "b2")
	for _, s := range []domain.AgentState{domain.StateWorking, domain.StateReady} {
		if _, err := st.Sessions().Transition(ctx, b.ID, s, domain.ConfidenceHook, "", domain.SourceHook); err != nil {
			t.Fatal(err)
		}
		advance(time.Millisecond)
	}
	c, ok, _ := st.Messages().Claim(ctx, b.ID, 0)
	if !ok || c.Message.Body != "b2" {
		t.Fatalf("next claim = %+v %v", c, ok)
	}
}

func TestAckFromHook(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	sess := readySession(t, st, "demo")
	m := enqueue(t, st, sess, "hello")
	ms := st.Messages()
	if d, err := ms.AckFromHook(ctx, sess.ID, sha("hello"), st.Now(), time.Minute, 3); err != nil || d.MessageID != 0 {
		t.Fatalf("queued message acked: %+v %v", d, err)
	}
	if _, ok, _ := ms.Claim(ctx, sess.ID, 0); !ok {
		t.Fatal("claim")
	}
	if _, err := ms.MarkSent(ctx, m.ID, st.Now(), 0); err != nil {
		t.Fatal(err)
	}
	d, err := ms.AckFromHook(ctx, sess.ID, sha("[Pasted text #1]"), st.Now(), time.Minute, 3)
	if err != nil || d.MessageID != m.ID || d.Match != domain.AckTiming {
		t.Fatalf("timing ack = %+v %v", d, err)
	}
	if got, _ := ms.Get(ctx, m.ID); got.State != domain.MsgAcknowledged || got.AckMatch != domain.AckTiming {
		t.Fatalf("acked = %+v", got)
	}
	if d, _ := ms.AckFromHook(ctx, sess.ID, sha("hello"), st.Now(), time.Minute, 3); d.MessageID != 0 {
		t.Fatalf("acknowledged twice: %+v", d)
	}
}

func TestQueuedSessions(t *testing.T) {
	st, _ := openTest(t)
	a := readySession(t, st, "a")
	readySession(t, st, "b")
	enqueue(t, st, a, "x")
	ids, err := st.Messages().QueuedSessions(context.Background())
	if err != nil || len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("queued sessions = %v %v", ids, err)
	}
	list, err := st.Messages().ForTask(context.Background(), "a", 10)
	if err != nil || len(list) != 1 || list[0].Body != "x" {
		t.Fatalf("for task = %+v %v", list, err)
	}
}
