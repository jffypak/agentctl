package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
)

// openAtInit creates a registry with only 0001_init applied, as a Phase-1
// install has it, and returns the raw handle.
func openAtInit(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agentctl.db")
	if err := ensurePrivateFile(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn(path, DefaultBusyTimeout))
	if err != nil {
		t.Fatal(err)
	}
	init, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations(fstest.MapFS{"0001_init.sql": {Data: init}})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db, ms, time.Now); err != nil {
		t.Fatalf("migrate 0001: %v", err)
	}
	return db, path
}

func TestMigration1000KeepsRows(t *testing.T) {
	ctx := context.Background()
	db, path := openAtInit(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO repos (id, root_path, name, created_at) VALUES (1, '/r', 'r', 1)`)
	exec(`INSERT INTO tasks (id, repo_id, name, goal, state, worktree_path, branch, created_at, updated_at)
	      VALUES ('a', 1, 'a', 'g', 'running', '/r-w/a', 'jpark/a', 1, 1),
	             ('b', 1, 'b', '', 'running', '/r', 'main', 1, 1)`)
	exec(`INSERT INTO sessions (id, task_id, agent_type, origin, tmux_socket, tmux_server_pid, tmux_pane_id, pane_pid,
	      agent_session_id, agent_state, state_confidence, state_since, last_detail, created_at)
	      VALUES (10, 'a', 'claude', 'launched', '/s', 5, '%1', 50, 'sid', 'ready', 'hook', 2000, 'turn finished', 1000),
	             (11, 'b', 'claude', 'adopted', '/s', 5, '%2', 51, '', 'unknown', 'none', 1500, 'adopted; no hooks yet', 1500)`)
	exec(`INSERT INTO messages (id, task_id, session_id, body, body_sha256, state, created_at)
	      VALUES (100, 'a', 10, 'hi', 'h', 'queued', 3000)`)
	exec(`INSERT INTO events (ts, type, task_id, session_id, payload) VALUES (1800, ?, 'a', 10, '{}'), (1900, ?, 'a', 10, '{}')`,
		domain.EventHookReceived, domain.EventHookReceived)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	st, err := Open(ctx, path, Options{Now: newFakeClock().Now})
	if err != nil {
		t.Fatalf("Open (applies 1000): %v", err)
	}
	defer st.Close()
	applied, err := st.AppliedMigrations(ctx)
	if err != nil || len(applied) != 2 || applied[1] != 1000 {
		t.Fatalf("applied = %v, %v; want [1 1000]", applied, err)
	}

	a, err := st.Sessions().Get(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if a.TaskID != "a" || a.Origin != domain.OriginLaunched || a.State != domain.StateReady ||
		a.AgentSessionID != "sid" || a.LastDetail != "turn finished" || a.Identity.PaneID != "%1" {
		t.Errorf("session 10 changed: %+v", a)
	}
	if !a.HasHook() || !a.FirstHookAt.Equal(time.UnixMilli(1800).UTC()) {
		t.Errorf("session 10: HasHook %v FirstHookAt %v, want backfilled from the first hook.received (1800)", a.HasHook(), a.FirstHookAt)
	}
	b, err := st.Sessions().Get(ctx, 11)
	if err != nil {
		t.Fatal(err)
	}
	if b.Origin != domain.OriginAdopted || b.HasHook() {
		t.Errorf("session 11: origin %s HasHook %v, want adopted / false", b.Origin, b.HasHook())
	}

	var n int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = 100 AND session_id = 10`).Scan(&n); err != nil || n != 1 {
		t.Errorf("message 100: count %d, %v", n, err)
	}
	rows, err := st.DB().QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("foreign_key_check reports a violation after the migration")
	}
	rows.Close()
	for _, idx := range []string{"sessions_task", "sessions_pane", "sessions_state"} {
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after the migration", idx)
		}
	}

	if _, err := st.DB().ExecContext(ctx, `INSERT INTO sessions (task_id, agent_type, origin, agent_state, state_confidence, state_since, created_at)
	      VALUES ('b', 'codex', 'wrapped', 'starting', 'none', 1, 1)`); err != nil {
		t.Errorf("insert origin wrapped: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO sessions (task_id, agent_type, origin, agent_state, state_confidence, state_since, created_at)
	      VALUES ('b', 'codex', 'bogus', 'starting', 'none', 1, 1)`); err == nil {
		t.Error("insert origin bogus succeeded; the CHECK must still reject it")
	}
}

func TestRecordHookSetsFirstHookAt(t *testing.T) {
	ctx := context.Background()
	st, _ := openTest(t)
	seedTask(t, st, "t1")
	sess, err := st.Sessions().Create(ctx, NewSession{
		TaskID: "t1", AgentType: "codex", Origin: domain.OriginLaunched,
		Identity:    domain.Identity{Socket: "/s", ServerPID: 5, SessionID: "$1", PaneID: "%1", PanePID: 50},
		SessionName: "agentctl-t1", State: domain.StateStarting, Confidence: domain.ConfidenceNone,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.HasHook() {
		t.Fatal("a new session must not have a hook")
	}
	// A record-only hook (Codex SessionStart) counts as the first event.
	r1, err := st.Sessions().RecordHook(ctx, HookInput{SessionID: sess.ID, Event: "SessionStart", Detail: "session started (startup)"})
	if err != nil {
		t.Fatal(err)
	}
	if !r1.Session.HasHook() || r1.Session.FirstHookAt.IsZero() {
		t.Fatalf("after the first hook: HasHook %v FirstHookAt %v", r1.Session.HasHook(), r1.Session.FirstHookAt)
	}
	first := r1.Session.FirstHookAt
	r2, err := st.Sessions().RecordHook(ctx, HookInput{SessionID: sess.ID, Event: "UserPromptSubmit", State: domain.StateWorking, Detail: "prompt submitted"})
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Session.FirstHookAt.Equal(first) {
		t.Errorf("FirstHookAt moved from %v to %v; it must keep the first hook's time", first, r2.Session.FirstHookAt)
	}
}

func TestRegisterAdoptedAcceptsWrapped(t *testing.T) {
	ctx := context.Background()
	st, _ := openTest(t)
	repo, err := st.Repos().Upsert(ctx, "/repos/main", "main")
	if err != nil {
		t.Fatal(err)
	}
	id := domain.Identity{Socket: "/s", ServerPID: 5, SessionID: "$1", PaneID: "%4", PanePID: 54}
	_, sess, err := st.Sessions().RegisterAdopted(ctx,
		Task{ID: "w", RepoID: repo.ID, WorktreePath: "/repos/main", Branch: "main"},
		NewSession{TaskID: "w", AgentType: "codex", Origin: domain.OriginWrapped, Identity: id,
			State: domain.StateStarting, Confidence: domain.ConfidenceNone, Detail: domain.DetailAwaitingFirstPrompt})
	if err != nil {
		t.Fatalf("RegisterAdopted wrapped: %v", err)
	}
	if sess.Origin != domain.OriginWrapped || sess.State != domain.StateStarting || sess.AgentType != "codex" {
		t.Errorf("got %+v", sess)
	}
	// Adopted still requires unknown; wrapped requires starting.
	_, _, err = st.Sessions().RegisterAdopted(ctx,
		Task{ID: "w2", RepoID: repo.ID, WorktreePath: "/repos/main", Branch: "main"},
		NewSession{TaskID: "w2", AgentType: "codex", Origin: domain.OriginWrapped,
			Identity: domain.Identity{Socket: "/s", ServerPID: 5, SessionID: "$1", PaneID: "%5", PanePID: 55},
			State:    domain.StateUnknown, Confidence: domain.ConfidenceNone})
	if err == nil {
		t.Error("wrapped with state unknown was accepted")
	}
}

// A migration marked foreign_keys=off runs with enforcement off (so a table
// rebuild can drop and rename a parent table) but must still leave no
// dangling reference: the check runs before commit and a violation rolls
// the whole migration back.
func TestForeignKeysOffMigrationStillChecks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fk.db")
	db, err := sql.Open("sqlite", dsn(path, DefaultBusyTimeout))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	base, err := loadMigrations(fstest.MapFS{
		"0001_base.sql": {Data: []byte(`CREATE TABLE p (id INTEGER PRIMARY KEY);
			CREATE TABLE c (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES p(id));
			INSERT INTO p VALUES (1); INSERT INTO c VALUES (10, 1);`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, base, time.Now); err != nil {
		t.Fatal(err)
	}
	bad, err := loadMigrations(fstest.MapFS{
		"0002_bad.sql": {Data: []byte(MigrationForeignKeysOff + "\nDELETE FROM p;")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, bad, time.Now); err == nil {
		t.Fatal("a foreign_keys=off migration that leaves a dangling reference must fail")
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM p`).Scan(&n); err != nil || n != 1 {
		t.Errorf("parent rows after rollback = %d (%v), want 1", n, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 2`).Scan(&n); err != nil || n != 0 {
		t.Errorf("version 2 recorded = %d (%v), want 0", n, err)
	}
	var on int
	if err := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&on); err != nil || on != 1 {
		t.Errorf("PRAGMA foreign_keys = %d (%v) after the migration, want 1", on, err)
	}
}

// A pane whose latest session is a finished wrapped one can be registered
// again as a new wrapped task (agentctl codex after cd or close); any other
// owner still blocks it.
func TestRegisterAdoptedWrappedAfterFinishedWrapped(t *testing.T) {
	ctx := context.Background()
	st, _ := openTest(t)
	repo, _ := st.Repos().Upsert(ctx, "/repos/main", "main")
	id := domain.Identity{Socket: "/s", ServerPID: 5, SessionID: "$1", PaneID: "%4", PanePID: 54}
	ns := func(task string, o domain.Origin, s domain.AgentState) NewSession {
		return NewSession{TaskID: task, AgentType: "codex", Origin: o, Identity: id, State: s, Confidence: domain.ConfidenceNone}
	}
	_, first, err := st.Sessions().RegisterAdopted(ctx, Task{ID: "a", RepoID: repo.ID, WorktreePath: "/w", Branch: "main"}, ns("a", domain.OriginWrapped, domain.StateStarting))
	if err != nil {
		t.Fatal(err)
	}
	task := func(id string) Task { return Task{ID: id, RepoID: repo.ID, WorktreePath: "/w2", Branch: "main"} }
	if _, _, err := st.Sessions().RegisterAdopted(ctx, task("b"), ns("b", domain.OriginWrapped, domain.StateStarting)); !errors.Is(err, ErrPaneRegistered) {
		t.Fatalf("live wrapped owner: err = %v, want ErrPaneRegistered", err)
	}
	if _, err := st.Sessions().ReconcileTransition(ctx, ReconcileInput{SessionID: first.ID, To: domain.StateStopped, Confidence: domain.ConfidencePane, Source: domain.SourceWrapper}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Sessions().RegisterAdopted(ctx, task("c"), ns("c", domain.OriginAdopted, domain.StateUnknown)); !errors.Is(err, ErrPaneRegistered) {
		t.Fatalf("adopt over a finished wrapped pane: err = %v, want ErrPaneRegistered", err)
	}
	if _, _, err := st.Sessions().RegisterAdopted(ctx, task("d"), ns("d", domain.OriginWrapped, domain.StateStarting)); err != nil {
		t.Fatalf("wrapped over a finished wrapped pane: %v", err)
	}
}
