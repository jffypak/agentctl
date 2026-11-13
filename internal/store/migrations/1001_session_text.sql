-- command-center (m4+ range, ARCHITECTURE.md §Code conventions 3).
-- 1. sessions.last_prompt / last_message: the newest prompt the agent
--    received and its newest final reply, from hooks (UserPromptSubmit
--    prompt, Stop last_assistant_message). Sanitized and capped at
--    adapter.MaxHookText bytes. Shown in the task details.
-- 2. sessions.model: the model the agent reported (SessionStart and later
--    hooks), '' when unknown.
-- 3. sessions.turn_ended_at: when the agent last finished a turn (Stop /
--    StopFailure), NULL before the first one.
-- 4. tasks.viewed_at: when the user last looked at the task (attach, dock,
--    detail, logs, prompt). A turn that ended after it is unread.

ALTER TABLE sessions ADD COLUMN last_prompt   TEXT    NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN last_message  TEXT    NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN model         TEXT    NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN turn_ended_at INTEGER;
ALTER TABLE tasks    ADD COLUMN viewed_at     INTEGER;
