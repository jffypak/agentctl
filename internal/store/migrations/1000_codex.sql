-- agentctl:foreign_keys=off
-- codex-support (m4+ range, ARCHITECTURE.md §Code conventions 3).
-- 1. sessions.origin also allows 'wrapped' (`agentctl codex` in the user's pane).
-- 2. sessions.first_hook_at: time of the first hook.received matched to the
--    session (NULL = none yet). Drives the INITIALSTATE attention section.
-- SQLite cannot alter a CHECK constraint, so the table is rebuilt with the
-- same ids. messages.session_id references sessions, so the migration runs
-- with enforcement off (first line; see store.MigrationForeignKeysOff) and
-- the runner's PRAGMA foreign_key_check must pass before commit.

CREATE TABLE sessions_new (
    id                 INTEGER PRIMARY KEY,
    task_id            TEXT    NOT NULL REFERENCES tasks(id),
    agent_type         TEXT    NOT NULL,
    origin             TEXT    NOT NULL CHECK (origin IN ('launched', 'adopted', 'wrapped')),
    -- identity, captured at launch/adoption; empty only for a failed launch
    tmux_socket        TEXT    NOT NULL DEFAULT '',
    tmux_server_pid    INTEGER NOT NULL DEFAULT 0,
    tmux_session_id    TEXT    NOT NULL DEFAULT '',
    tmux_session_name  TEXT    NOT NULL DEFAULT '',
    tmux_pane_id       TEXT    NOT NULL DEFAULT '',
    pane_pid           INTEGER NOT NULL DEFAULT 0,
    agent_session_id   TEXT    NOT NULL DEFAULT '',  -- from hooks
    agent_state        TEXT    NOT NULL CHECK (agent_state IN (
                           'starting', 'ready', 'working', 'awaiting_input',
                           'unknown', 'stopped', 'failed', 'disconnected')),
    state_confidence   TEXT    NOT NULL CHECK (state_confidence IN ('hook', 'pane', 'none')),
    state_since        INTEGER NOT NULL,
    last_detail        TEXT    NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    ended_at           INTEGER,
    first_hook_at      INTEGER
);

INSERT INTO sessions_new (id, task_id, agent_type, origin, tmux_socket, tmux_server_pid, tmux_session_id,
    tmux_session_name, tmux_pane_id, pane_pid, agent_session_id, agent_state, state_confidence,
    state_since, last_detail, created_at, ended_at, first_hook_at)
SELECT s.id, s.task_id, s.agent_type, s.origin, s.tmux_socket, s.tmux_server_pid, s.tmux_session_id,
    s.tmux_session_name, s.tmux_pane_id, s.pane_pid, s.agent_session_id, s.agent_state, s.state_confidence,
    s.state_since, s.last_detail, s.created_at, s.ended_at,
    (SELECT MIN(e.ts) FROM events e WHERE e.session_id = s.id AND e.type = 'hook.received')
FROM sessions s;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

CREATE INDEX sessions_task ON sessions (task_id, id);
CREATE INDEX sessions_pane ON sessions (tmux_socket, tmux_pane_id);
CREATE INDEX sessions_state ON sessions (agent_state);
