package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
)

// fakeClock is a deterministic, concurrency-safe clock that advances 1s per read.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentctl.db")
	st, err := Open(context.Background(), path, Options{Now: newFakeClock().Now})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, path
}

// seedTask creates a repo and a reserved running task.
func seedTask(t *testing.T, st *Store, id string) Task {
	t.Helper()
	ctx := context.Background()
	repo, err := st.Repos().Upsert(ctx, "/repos/main", "main")
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	task, err := st.Tasks().Reserve(ctx, Task{
		ID: id, RepoID: repo.ID, Name: id, Goal: "do " + id,
		WorktreePath: "/repos/main-worktrees/" + id, Branch: "agentctl/" + id,
		BaseRef: "main", BaseSHA: "abc123",
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return task
}

func seedSession(t *testing.T, st *Store, taskID string, state domain.AgentState) Session {
	t.Helper()
	sess, err := st.Sessions().Create(context.Background(), NewSession{
		TaskID: taskID, AgentType: "claude", Origin: domain.OriginLaunched,
		Identity:    domain.Identity{Socket: "/tmp/tmux-1/default", ServerPID: 10, SessionID: "$1", PaneID: "%1", PanePID: 20},
		SessionName: "agentctl-" + taskID,
		State:       state, Confidence: domain.ConfidenceNone,
	})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	return sess
}

func countRows(t *testing.T, st *Store, table string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestOpenCreatesPrivateFileInWALMode(t *testing.T) {
	st, path := openTest(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("db mode = %o, want 600", mode)
	}
	var jm string
	if err := st.DB().QueryRow("PRAGMA journal_mode").Scan(&jm); err != nil {
		t.Fatal(err)
	}
	if jm != "wal" {
		t.Fatalf("journal_mode = %q, want wal", jm)
	}
	seedTask(t, st, "demo") // force a WAL write
	for _, side := range []string{path + "-wal", path + "-shm"} {
		if fi, err := os.Stat(side); err == nil && fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode = %o, want no group/other access", side, fi.Mode().Perm())
		}
	}
	var fk int
	if err := st.DB().QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d (%v), want 1", fk, err)
	}
}

func TestOpenTightensExistingFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentctl.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestBusyTimeoutOption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agentctl.db")
	for _, c := range []struct {
		opt  Options
		want int
	}{{Options{}, 5000}, {Options{BusyTimeout: 1500 * time.Millisecond}, 1500}} {
		st, err := Open(context.Background(), path, c.opt)
		if err != nil {
			t.Fatal(err)
		}
		var got int
		if err := st.DB().QueryRow("PRAGMA busy_timeout").Scan(&got); err != nil {
			t.Fatal(err)
		}
		_ = st.Close()
		if got != c.want {
			t.Errorf("busy_timeout = %d, want %d", got, c.want)
		}
	}
}

func TestReopenKeepsAppliedSetAndData(t *testing.T) {
	st, path := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	before, err := st.AppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 || before[0] != 1 {
		t.Fatalf("applied = %v, want [1 ...]", before)
	}
	_ = st.Close()

	st2, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	after, err := st2.AppliedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || after[0] != before[0] {
		t.Fatalf("applied after reopen = %v, before = %v", after, before)
	}
	if _, err := st2.Tasks().Get(ctx, "demo"); err != nil {
		t.Fatalf("task lost after reopen: %v", err)
	}
}

func TestMigratorAppliesUnappliedLowerVersion(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	// No IF NOT EXISTS: re-running an applied migration would fail loudly.
	fsys := fstest.MapFS{
		"0101_one.sql":   {Data: []byte("CREATE TABLE t101 (x INTEGER);")},
		"0103_three.sql": {Data: []byte("CREATE TABLE t103 (x INTEGER); INSERT INTO t103 VALUES (1);")},
	}
	ms, err := loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, st.DB(), ms, st.now); err != nil {
		t.Fatal(err)
	}
	fsys["0102_two.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE t102 (x INTEGER);")}
	ms, err = loadMigrations(fsys)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, st.DB(), ms, st.now); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	applied, _ := st.AppliedMigrations(ctx)
	want := []int{1, 101, 102, 103}
	if len(applied) != len(want) {
		t.Fatalf("applied = %v, want %v", applied, want)
	}
	for i := range want {
		if applied[i] != want[i] {
			t.Fatalf("applied = %v, want %v", applied, want)
		}
	}
	if n := countRows(t, st, "t102"); n != 0 {
		t.Fatalf("t102 rows = %d", n)
	}
	if n := countRows(t, st, "t103"); n != 1 {
		t.Fatalf("t103 rows = %d, want 1 (applied exactly once)", n)
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	ms, _ := loadMigrations(fstest.MapFS{
		"0200_bad.sql": {Data: []byte("CREATE TABLE t200 (x INTEGER); THIS IS NOT SQL;")},
	})
	if err := migrate(ctx, st.DB(), ms, st.now); err == nil {
		t.Fatal("bad migration applied without error")
	}
	applied, _ := st.AppliedMigrations(ctx)
	for _, v := range applied {
		if v == 200 {
			t.Fatal("failed migration recorded as applied")
		}
	}
	var n int
	_ = st.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='t200'").Scan(&n)
	if n != 0 {
		t.Fatal("partial migration left table t200 behind")
	}
}

func TestLoadMigrationsValidatesNames(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"no number": {"init.sql": {Data: []byte("SELECT 1;")}},
		"duplicate": {"0001_a.sql": {Data: []byte("SELECT 1;")}, "0001_b.sql": {Data: []byte("SELECT 1;")}},
		"zero":      {"0000_a.sql": {Data: []byte("SELECT 1;")}},
	} {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	ms, err := loadMigrations(fstest.MapFS{"README": {Data: []byte("x")}, "0002_b.sql": {Data: []byte("x")}, "0001_a.sql": {Data: []byte("y")}})
	if err != nil || len(ms) != 2 || ms[0].Version != 1 || ms[1].Version != 2 {
		t.Fatalf("got %v %v, want sorted [1 2] ignoring non-.sql", ms, err)
	}
}

func TestEmbeddedSchemaHasEveryPhase1Table(t *testing.T) {
	st, _ := openTest(t)
	for _, table := range []string{"schema_migrations", "repos", "tasks", "sessions", "messages", "events"} {
		var n int
		if err := st.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (%v)", table, err)
		}
	}
}

func TestReposUpsertIsIdempotent(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	a, err := st.Repos().Upsert(ctx, "/r/x", "x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Repos().Upsert(ctx, "/r/x", "x")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.ID == 0 {
		t.Fatalf("ids %d %d", a.ID, b.ID)
	}
	got, err := st.Repos().ByPath(ctx, "/r/x")
	if err != nil || got.ID != a.ID || got.Name != "x" {
		t.Fatalf("ByPath = %+v %v", got, err)
	}
	if _, err := st.Repos().ByPath(ctx, "/nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ByPath missing = %v, want ErrNotFound", err)
	}
	if _, err := st.Repos().Upsert(ctx, "relative", "x"); err == nil {
		t.Fatal("relative root path accepted")
	}
}

func TestTasksReserveGetUnreserve(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	task := seedTask(t, st, "demo")
	if task.State != domain.TaskRunning || task.CreatedAt.IsZero() {
		t.Fatalf("reserved task = %+v", task)
	}
	_, err := st.Tasks().Reserve(ctx, Task{ID: "demo", RepoID: task.RepoID, Name: "demo", WorktreePath: "/x", Branch: "b"})
	if !errors.Is(err, ErrTaskExists) {
		t.Fatalf("duplicate Reserve = %v, want ErrTaskExists", err)
	}
	if _, err := st.Tasks().Reserve(ctx, Task{ID: "Bad.Name", RepoID: task.RepoID, Name: "x", WorktreePath: "/x", Branch: "b"}); !errors.Is(err, domain.ErrInvalidSlug) {
		t.Fatalf("invalid slug Reserve = %v", err)
	}
	got, err := st.Tasks().Get(ctx, "demo")
	if err != nil || got.Goal != "do demo" || got.BaseSHA != "abc123" {
		t.Fatalf("Get = %+v %v", got, err)
	}
	if _, err := st.Tasks().Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v", err)
	}
	ok, err := st.Tasks().Exists(ctx, "demo")
	if err != nil || !ok {
		t.Fatalf("Exists = %v %v", ok, err)
	}

	seedSession(t, st, "demo", domain.StateStarting)
	if err := st.Tasks().Unreserve(ctx, "demo"); !errors.Is(err, ErrTaskHasSession) {
		t.Fatalf("Unreserve with session = %v, want ErrTaskHasSession", err)
	}
	seedTask(t, st, "other")
	if err := st.Tasks().Unreserve(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Tasks().Exists(ctx, "other"); ok {
		t.Fatal("unreserved task still exists")
	}
}

func TestSessionsCreateAppendsEventsAtomically(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess, err := st.Sessions().Create(ctx, NewSession{
		TaskID: "demo", AgentType: "claude", Origin: domain.OriginLaunched,
		Identity: domain.Identity{Socket: "/s", ServerPID: 1, SessionID: "$0", PaneID: "%0", PanePID: 2},
		State:    domain.StateStarting, Confidence: domain.ConfidenceNone,
		Events: []EventInput{
			{Type: domain.EventTaskCreated, Payload: map[string]any{"branch": "agentctl/demo"}},
			{Type: domain.EventSessionStarted},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	evs, err := st.Events().ForTask(ctx, "demo")
	if err != nil || len(evs) != 2 {
		t.Fatalf("events = %v %v", evs, err)
	}
	if evs[0].Type != domain.EventTaskCreated || evs[1].Type != domain.EventSessionStarted {
		t.Fatalf("event order = %s, %s", evs[0].Type, evs[1].Type)
	}
	for _, e := range evs {
		if e.TaskID != "demo" || e.SessionID != sess.ID {
			t.Fatalf("event ids not filled: %+v", e)
		}
	}
	got, err := st.Sessions().Get(ctx, sess.ID)
	if err != nil || got.Identity != sess.Identity || got.State != domain.StateStarting {
		t.Fatalf("Get = %+v %v", got, err)
	}
	latest, err := st.Sessions().LatestForTask(ctx, "demo")
	if err != nil || latest.ID != sess.ID {
		t.Fatalf("LatestForTask = %+v %v", latest, err)
	}

	// An invalid event rolls back the session insert too.
	_, err = st.Sessions().Create(ctx, NewSession{
		TaskID: "demo", AgentType: "claude", Origin: domain.OriginLaunched,
		State: domain.StateStarting, Confidence: domain.ConfidenceNone,
		Events: []EventInput{{Type: ""}},
	})
	if err == nil {
		t.Fatal("empty event type accepted")
	}
	if n := countRows(t, st, "sessions"); n != 1 {
		t.Fatalf("sessions = %d after failed create, want 1", n)
	}
}

func TestSessionsCreateRejectsNonInitialStates(t *testing.T) {
	st, _ := openTest(t)
	seedTask(t, st, "demo")
	for _, s := range []domain.AgentState{domain.StateReady, domain.StateWorking, domain.StateStopped, "bogus"} {
		_, err := st.Sessions().Create(context.Background(), NewSession{
			TaskID: "demo", AgentType: "claude", Origin: domain.OriginLaunched,
			State: s, Confidence: domain.ConfidenceNone,
		})
		if err == nil {
			t.Errorf("Create with initial state %s accepted", s)
		}
	}
	for _, s := range []domain.AgentState{domain.StateStarting, domain.StateUnknown, domain.StateFailed} {
		if _, err := st.Sessions().Create(context.Background(), NewSession{
			TaskID: "demo", AgentType: "claude", Origin: domain.OriginAdopted,
			State: s, Confidence: domain.ConfidenceNone,
		}); err != nil {
			t.Errorf("Create with initial state %s: %v", s, err)
		}
	}
}

func TestTransitionAllowedAppendsEvent(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)

	res, err := st.Sessions().Transition(ctx, sess.ID, domain.StateReady, domain.ConfidenceHook, "SessionStart", domain.SourceHook)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.From != domain.StateStarting || res.To != domain.StateReady || res.EventID == 0 {
		t.Fatalf("result = %+v", res)
	}
	got, _ := st.Sessions().Get(ctx, sess.ID)
	if got.State != domain.StateReady || got.Confidence != domain.ConfidenceHook || got.LastDetail != "SessionStart" {
		t.Fatalf("session = %+v", got)
	}
	if !got.StateSince.After(sess.StateSince) {
		t.Fatalf("state_since not advanced: %v -> %v", sess.StateSince, got.StateSince)
	}
	evs, _ := st.Events().ForTask(ctx, "demo")
	last := evs[len(evs)-1]
	if last.Type != domain.EventAgentStateChanged || last.ID != res.EventID || last.SessionID != sess.ID {
		t.Fatalf("last event = %+v", last)
	}
	var p map[string]string
	if err := json.Unmarshal(last.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["from"] != "starting" || p["to"] != "ready" || p["source"] != "hook" || p["confidence"] != "hook" {
		t.Fatalf("payload = %v", p)
	}
}

func TestTransitionRejectsDisallowed(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateStopped, domain.ConfidenceHook, "SessionEnd", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	stopped, _ := st.Sessions().Get(ctx, sess.ID)
	if stopped.EndedAt.IsZero() {
		t.Fatal("ended_at not set on terminal transition")
	}
	before := countRows(t, st, "events")
	for _, to := range []domain.AgentState{domain.StateReady, domain.StateWorking, domain.StateStarting, domain.StateFailed} {
		_, err := st.Sessions().Transition(ctx, sess.ID, to, domain.ConfidenceHook, "late hook", domain.SourceHook)
		if !errors.Is(err, ErrTransitionNotAllowed) {
			t.Fatalf("stopped -> %s = %v, want ErrTransitionNotAllowed", to, err)
		}
	}
	if n := countRows(t, st, "events"); n != before {
		t.Fatalf("rejected transitions appended %d events", n-before)
	}
	got, _ := st.Sessions().Get(ctx, sess.ID)
	if got.State != domain.StateStopped || !got.StateSince.Equal(stopped.StateSince) || got.LastDetail != stopped.LastDetail {
		t.Fatalf("rejected transition changed row: %+v", got)
	}
	if _, err := st.Sessions().Transition(ctx, 9999, domain.StateReady, domain.ConfidenceHook, "", domain.SourceHook); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session = %v", err)
	}
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateStopped, "bogus", "", domain.SourceHook); err == nil {
		t.Fatal("invalid confidence accepted")
	}
}

func TestTransitionSelfIsNoOp(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateUnknown)
	res, err := st.Sessions().Transition(ctx, sess.ID, domain.StateWorking, domain.ConfidenceHook, "PreToolUse", domain.SourceHook)
	if err != nil || !res.Changed {
		t.Fatalf("unknown -> working = %+v %v", res, err)
	}
	mid, _ := st.Sessions().Get(ctx, sess.ID)
	before := countRows(t, st, "events")
	res, err = st.Sessions().Transition(ctx, sess.ID, domain.StateWorking, domain.ConfidenceHook, "PostToolUse", domain.SourceHook)
	if err != nil || res.Changed || res.EventID != 0 {
		t.Fatalf("working -> working = %+v %v", res, err)
	}
	got, _ := st.Sessions().Get(ctx, sess.ID)
	if !got.StateSince.Equal(mid.StateSince) {
		t.Fatal("self transition reset state_since")
	}
	if got.LastDetail != "PostToolUse" {
		t.Fatalf("self transition did not refresh detail: %q", got.LastDetail)
	}
	if n := countRows(t, st, "events"); n != before {
		t.Fatal("self transition appended an event")
	}
}

func TestTransitionIsAtomicWithEventAppend(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	if _, err := st.DB().Exec(`CREATE TRIGGER fail_state_events BEFORE INSERT ON events
		WHEN NEW.type = 'agent.state_changed' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateReady, domain.ConfidenceHook, "x", domain.SourceHook); err == nil {
		t.Fatal("transition succeeded although its event append failed")
	}
	got, _ := st.Sessions().Get(ctx, sess.ID)
	if got.State != domain.StateStarting {
		t.Fatalf("state = %s, want starting (rolled back)", got.State)
	}
}

func TestTransitionSanitizesDetail(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "demo")
	sess := seedSession(t, st, "demo", domain.StateStarting)
	long := make([]byte, 2000)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := st.Sessions().Transition(ctx, sess.ID, domain.StateAwaitingInput, domain.ConfidenceHook,
		"\x1b]52;c;AAAA\x07perm\x1b[31m prompt"+string(long), domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Sessions().Get(ctx, sess.ID)
	if got.LastDetail[:11] != "perm prompt" || len(got.LastDetail) > MaxDetailLen {
		t.Fatalf("detail = %q (len %d)", got.LastDetail[:20], len(got.LastDetail))
	}
}

func TestListActive(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "a")
	seedTask(t, st, "b")
	seedTask(t, st, "c")
	sa := seedSession(t, st, "a", domain.StateStarting)
	sb := seedSession(t, st, "b", domain.StateStarting)
	seedSession(t, st, "c", domain.StateFailed)
	if _, err := st.Sessions().Transition(ctx, sb.ID, domain.StateStopped, domain.ConfidencePane, "exit 0", domain.SourcePane); err != nil {
		t.Fatal(err)
	}
	active, err := st.Sessions().ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != sa.ID {
		t.Fatalf("active = %+v, want only session of a", active)
	}
}

func TestEventsAppendSinceLastID(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	if id, err := st.Events().LastID(ctx); err != nil || id != 0 {
		t.Fatalf("LastID empty = %d %v", id, err)
	}
	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := st.Events().Append(ctx, EventInput{Type: domain.EventUIOpened, Payload: map[string]any{"i": i}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	cause, err := st.Events().Append(ctx, EventInput{Type: domain.EventHookReceived, TaskID: "t", SessionID: 7, CauseID: ids[0]})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Events().Since(ctx, ids[1], 2)
	if err != nil || len(got) != 2 || got[0].ID != ids[2] || got[1].ID != ids[3] {
		t.Fatalf("Since = %+v %v", got, err)
	}
	all, _ := st.Events().Since(ctx, 0, 0)
	if len(all) != 6 {
		t.Fatalf("Since(0) = %d events", len(all))
	}
	last := all[5]
	if last.ID != cause || last.CauseID != ids[0] || last.SessionID != 7 || last.TaskID != "t" || last.TS.IsZero() {
		t.Fatalf("last = %+v", last)
	}
	if string(all[0].Payload) != `{"i":0}` {
		t.Fatalf("payload = %s", all[0].Payload)
	}
	if string(last.Payload) != `{}` {
		t.Fatalf("nil payload stored as %s", last.Payload)
	}
	if id, _ := st.Events().LastID(ctx); id != cause {
		t.Fatalf("LastID = %d, want %d", id, cause)
	}
	if _, err := st.Events().Append(ctx, EventInput{Type: "nodot"}); err == nil {
		t.Fatal("undotted event type accepted")
	}
}

func TestOverviewJoinsSessionsAndMessageCounts(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	seedTask(t, st, "a")
	seedTask(t, st, "b")
	seedTask(t, st, "c")
	sa := seedSession(t, st, "a", domain.StateStarting)
	seedSession(t, st, "b", domain.StateUnknown)
	// raw message fixtures (store/messages.go belongs to m2-prompt-queue)
	for _, m := range []struct {
		task  string
		sess  int64
		state string
	}{{"a", sa.ID, "queued"}, {"a", sa.ID, "queued"}, {"a", sa.ID, "unconfirmed"}, {"a", sa.ID, "acknowledged"}} {
		if _, err := st.DB().Exec(`INSERT INTO messages (task_id, session_id, body, body_sha256, state, created_at) VALUES (?, ?, 'b', 'h', ?, 0)`, m.task, m.sess, m.state); err != nil {
			t.Fatal(err)
		}
	}
	other, _ := st.Repos().Upsert(ctx, "/repos/other", "other")
	if _, err := st.Tasks().Reserve(ctx, Task{ID: "z", RepoID: other.ID, Name: "z", WorktreePath: "/w", Branch: "agentctl/z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE tasks SET state='closed', closed_at=1 WHERE id='c'`); err != nil {
		t.Fatal(err)
	}

	rows, err := st.Overview(ctx, OverviewFilter{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]OverviewRow{}
	for _, r := range rows {
		byID[r.Task.ID] = r
	}
	if len(rows) != 3 || byID["c"].Task.ID != "" {
		t.Fatalf("rows = %d (%v), want a, b, z (closed hidden)", len(rows), keys(byID))
	}
	a := byID["a"]
	if a.Session == nil || a.Session.ID != sa.ID || a.Repo.Name != "main" {
		t.Fatalf("a = %+v", a)
	}
	if a.Messages.Queued != 2 || a.Messages.Unconfirmed != 1 || a.Messages.Acknowledged != 1 || a.Messages.Sent != 0 {
		t.Fatalf("a messages = %+v", a.Messages)
	}
	if z := byID["z"]; z.Session != nil || z.Repo.Name != "other" {
		t.Fatalf("z = %+v", z)
	}

	scoped, _ := st.Overview(ctx, OverviewFilter{RepoID: other.ID})
	if len(scoped) != 1 || scoped[0].Task.ID != "z" {
		t.Fatalf("scoped = %+v", scoped)
	}
	withClosed, _ := st.Overview(ctx, OverviewFilter{IncludeClosed: true})
	if len(withClosed) != 4 {
		t.Fatalf("with closed = %d rows", len(withClosed))
	}
}

func keys(m map[string]OverviewRow) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
