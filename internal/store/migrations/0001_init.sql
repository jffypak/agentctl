-- agentctl Phase-1 schema. This file is the whole Phase-1 schema; later cards
-- add no migrations (reserved ranges in ARCHITECTURE.md §Code conventions 3).
-- Timestamps are INTEGER unix milliseconds (UTC). Optional timestamps are NULL.

CREATE TABLE repos (
    id          INTEGER PRIMARY KEY,
    root_path   TEXT    NOT NULL UNIQUE,  -- main worktree root, realpath
    name        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE TABLE tasks (
    id             TEXT    PRIMARY KEY,   -- slug [a-z0-9][a-z0-9-]{0,39}
    repo_id        INTEGER NOT NULL REFERENCES repos(id),
    name           TEXT    NOT NULL,
    goal           TEXT    NOT NULL DEFAULT '',
    state          TEXT    NOT NULL CHECK (state IN ('running', 'closed')),
    worktree_path  TEXT    NOT NULL,
    branch         TEXT    NOT NULL,
    base_ref       TEXT    NOT NULL DEFAULT '',
    base_sha       TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL,
    closed_at      INTEGER
);
CREATE INDEX tasks_repo_state ON tasks (repo_id, state);

CREATE TABLE sessions (
    id                 INTEGER PRIMARY KEY,
    task_id            TEXT    NOT NULL REFERENCES tasks(id),
    agent_type         TEXT    NOT NULL,
    origin             TEXT    NOT NULL CHECK (origin IN ('launched', 'adopted')),
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
    ended_at           INTEGER
);
CREATE INDEX sessions_task ON sessions (task_id, id);
CREATE INDEX sessions_pane ON sessions (tmux_socket, tmux_pane_id);
CREATE INDEX sessions_state ON sessions (agent_state);

CREATE TABLE messages (
    id           INTEGER PRIMARY KEY,
    task_id      TEXT    NOT NULL REFERENCES tasks(id),
    session_id   INTEGER NOT NULL REFERENCES sessions(id),
    body         TEXT    NOT NULL,
    body_sha256  TEXT    NOT NULL,
    state        TEXT    NOT NULL CHECK (state IN (
                     'queued', 'delivering', 'sent', 'acknowledged',
                     'unconfirmed', 'failed', 'cancelled')),
    ack_match    TEXT    NOT NULL DEFAULT '' CHECK (ack_match IN ('', 'hash', 'timing')),
    created_at   INTEGER NOT NULL,
    claimed_at   INTEGER,
    sent_at      INTEGER,
    acked_at     INTEGER,
    error        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX messages_session_state ON messages (session_id, state, id);
CREATE INDEX messages_task_state ON messages (task_id, state);

-- Append-only. AUTOINCREMENT keeps ids strictly increasing and never reused,
-- so pollers can read `id > last` without missing rows.
CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER NOT NULL,
    type        TEXT    NOT NULL,
    task_id     TEXT    NOT NULL DEFAULT '',
    session_id  INTEGER,
    cause_id    INTEGER,
    payload     TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX events_type ON events (type, id);
CREATE INDEX events_task ON events (task_id, id);
CREATE INDEX events_session ON events (session_id, id);
