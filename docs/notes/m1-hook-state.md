# m1-hook-state — notes, payload fields relied on, findings

Hook-driven agent state: generated Claude settings, `agentctl hook`, `agentctl hooks-snippet`, `fakeagent`.
Gate: `scripts/doctor.sh`. No new modules, no migrations. Branch merged `main` (m1-pane-identity) once; the only
conflict was a duplicate `oneLine` helper in package `claude`, renamed `hookLine` in my `hook.go`.

## Claude Code hook payload fields relied on (2.1.x)

Source: the official hooks reference (code.claude.com/docs/en/hooks, read 2026-11-07). Fixtures shaped from it live in
`internal/adapter/claude/testdata/hooks/*.json`; replace them with real captures from `scripts/hook-probe.sh`.

| Field | Event(s) | Used for |
|---|---|---|
| `hook_event_name` | all | table key; must match `[A-Za-z0-9_]{1,64}` or the payload is rejected |
| `session_id` | all | `sessions.agent_session_id` (sanitized, ≤128 bytes) |
| `cwd` | all | `HookObservation.Cwd` (not stored) |
| `source` (`startup`/`resume`/`clear`/`compact`/`fork`) | SessionStart | `compact` = no change, else `ready` |
| `prompt` | UserPromptSubmit | hashed to `prompt_sha256` (hex sha256 of the exact string); the text is never kept |
| `tool_name` | PreToolUse, PostToolUse, PermissionRequest | `AskUserQuestion` → `awaiting_input`; detail |
| `notification_type`, `message` | Notification | `permission_prompt`/`elicitation_dialog` → `awaiting_input`, `idle_prompt` → `ready`; message → detail |
| `trigger` (`manual`/`auto`) | PreCompact | detail |
| `reason` | SessionEnd | detail only (see deviation 1) |
| `error` | StopFailure | recorded only (attr) |

Never decoded: `transcript_path` (never opened), `tool_input`, `tool_response`, `last_assistant_message`,
`permission_suggestions`. The decoder struct has no field for them. Settings shape relied on:
`{"hooks":{"<Event>":[{"hooks":[{"type":"command","command":"…","timeout":5}]}]}}`; an omitted matcher matches all;
`timeout` is seconds; command hooks run through a shell, so the path is single-quoted. Exit 0 plain stdout on
SessionStart/UserPromptSubmit is added to Claude's context, which is why the hook never writes stdout.

## How to run `scripts/hook-probe.sh` (director, once, by hand, inside tmux)

```sh
scripts/hook-probe.sh /tmp/probe          # starts `claude --settings /tmp/probe/probe-settings.json` in /tmp/probe/repo
# in the session: a Bash command needing permission (approve), an AskUserQuestion, a large multi-line paste,
# 60 s idle, /compact, /clear, then exit. The script prints this checklist.
scripts/hook-probe.sh --split /tmp/probe  # one file per payload in /tmp/probe/fixtures/<Event>_<discriminator>_<n>.json
```

It logs every mapped event plus PostToolUseFailure, PostCompact, PermissionDenied, StopFailure, Subagent*, Elicitation*
to `payloads.jsonl`, and the inherited `TMUX`/`TMUX_PANE`/`CLAUDE_PROJECT_DIR`/`AGENTCTL_*` to `env.log`. It never
touches `~/.claude`. `CLAUDE_BIN=fakeagent` dry-runs it (verified). Questions it answers: field names above; does
`prompt` carry a big paste or `[Pasted text #N]` (hash vs timing acks); does PermissionRequest fire for
AskUserQuestion; do `--settings` hooks run alongside user/project hooks; are `TMUX`/`TMUX_PANE` inherited.

## What exists now (APIs for later cards)

- **adapter/claude** — `HookEvents()`, `HookCommand(path)`, `SettingsJSON(path)`, `EnsureSettings(home)` (no rewrite
  when bytes + mode already match; else temp file + rename, 0600; hooks dir 0700), `ParseHook` (pure), `MaxHookDetail`,
  `PaneFromEnv(HookEnv) (HookPane, error)`, `HookPane.Matches(domain.Identity) (ok, reason)`.
- **store** (`sessions_hook.go`) — `Sessions.RecordHook(ctx, HookInput{SessionID, Event, AgentSessionID, State, Detail,
  Payload}) (HookResult{Session, ReceivedID, Transition, Applied, Refused}, error)`: `hook.received` + caused transition
  (confidence `hook`, source `hook`, `cause_id` = hook.received, extra `hook_event`) + `agent_session_id` in one
  BEGIN IMMEDIATE tx. Refused (sticky terminal) is not an error. `State == ""` + `Detail` refreshes `last_detail` only.
  `Sessions.ActiveByPane(ctx, paneID)`.
- **hookingest** — `Run(ctx, Config) Outcome`, `SettingsSnippet(path)`, constants `DefaultBudget` (1.8 s),
  `MaxPayload` (1 MiB), `ErrorLogName`, `DeliverLogName`, `Match*`, `Orphan*`; `Spawner` / `SpawnerFunc` /
  `ExecSpawner{Exe, Env, LogPath}` (Setsid, stdio → `logs/deliver.log`, released, never waited for).
- **The seam for m2-prompt-queue** (`hookingest/followup.go`): add `ack.go` / `trigger.go` with
  `var _ = registerFollowup(10, "ack", f)` / `registerFollowup(20, "trigger", f)`. A `Followup(ctx, Deps{Store, Spawner,
  PID, Now, Paths}, Applied{Observation, Session, Result, Match}) error` runs after the record, in order, inside the
  budget; errors and panics are logged, never undo the record. `Observation.PromptSHA256` is the ack hash;
  `Result.ReceivedID` is the `cause_id`; `Result.Transition.Changed && Session.State == ready` is the deliver trigger;
  `d.Spawner.SpawnDeliver(id, d.PID)`. No edit to existing hookingest files is needed.
- **testenv** — `BuildAgentctl(t)`, `BuildFakeAgent(t)`: one `go build` per test binary into
  `$TMPDIR/agentctl-testbin-<uid>/<checkout hash>/` (temp name + rename), then one warm-up exec (`help` / `-h`, empty
  env): the first exec of a fresh binary pays macOS's code scan (~250 ms idle, seconds under a loaded `go test ./...`),
  which otherwise lands inside the first timed hook or launch.
- **fakeagent** — per ARCHITECTURE.md, plus: `FAKEAGENT_LOG` entries carry `kind` (`paste` | `submit` | `key`) next to
  `{ts, text, state_at_receipt}`; `FAKEAGENT_HOOK_LOG` records every hook run `{ts, event, command, exit, stdout,
  stderr, ms}`; `#ask` fires PreToolUse → PermissionRequest → Notification permission_prompt and Enter in the dialog
  approves (the real danger of a blind paste); input received while not ready is logged and ignored, never run; pasted
  CRs become LFs (tmux paste-buffer turns LF into CR); SIGHUP/SIGTERM fire SessionEnd and exit 128+sig. tmux panes get
  the server's env, not the client's: pass `FAKEAGENT_*` in `AGENTCTL_AGENT_CMD` via `/usr/bin/env` (see the
  integration test).

## Director notes handled

1. **SessionEnd vs exit status.** SessionEnd now records `hook.received` and sets `last_detail` ("session ended
   (<reason>)") but never a state; reconcile turns the dead pane into `stopped` "exit 0" / `failed` "exit N" / "signal
   N". Also fixes a second bug: Claude fires SessionEnd `clear`/`resume` in a process that keeps running, and a sticky
   `stopped` there would have been permanent. Tests: `TestParseHookTable/session_end`, `TestRecordHookDetailOnly`,
   `TestTaskEnvHookAppliesState`, and end to end `TestHookStateEndToEnd` (`#exit 0` → stopped "exit 0"; goal
   `#exit 3`, which fires SessionEnd then exits 3 → failed "exit 3").
2. **Adopted-pane hooks.** With no `AGENTCTL_TASK_ID`, the hook parses `$TMUX` (socket, server pid; commas in the path
   are split off the right) and `$TMUX_PANE`, takes `ActiveByPane(pane id)` and keeps only sessions whose server pid
   matches and whose socket matches after `tmux.SameSocket` (both sides canonicalized with EvalSymlinks); pane pid is
   not used. Only `origin = adopted` sessions match; one match upgrades to confidence `hook` on its first state-setting
   hook. Tests: `TestPaneFromEnvAndMatch` (symlinked socket), `TestAdoptedPaneWithoutTaskEnvUpgradesConfidence`,
   `TestNoTaskEnvMatching`, and end to end `TestAdoptedPaneHooksViaSnippet` (pane started by hand with the
   hooks-snippet as settings, `agentctl adopt`, then unknown → working → ready, confidence hook, no orphans).

## Deviations from ARCHITECTURE.md / the card

1. **SessionEnd row**: "→ stopped" became "record + detail; terminal state from reconcile" (director note 1).
   Residual window: between SessionEnd and the pane dying the session can still read `ready`/`hook`; m2's Drain
   re-verifies a live pane before pasting, and claude exits within milliseconds of SessionEnd.
2. **Launch race** (new finding): `NewTask` inserts the session row only after tmux returns the identity, so a fast
   agent's SessionStart hook can arrive first. The hook now waits up to 1 s (20 ms polls) when the task exists but has
   no session yet; if none appears it records `hook.orphan{reason: no_session}`. Without this, fakeagent's SessionStart
   was lost and an agent launched without a goal would sit in `starting` until the 30 s timeout made it `unknown`.
   Alternative for the skeleton owner: insert the session before launch.
3. **Unmanaged panes record nothing.** A hook with neither `AGENTCTL_TASK_ID` nor a pane id any session knows (a user
   who merged hooks-snippet into `~/.claude/settings.json` running claude elsewhere) exits silently, with no
   `hook.orphan`, so ordinary claude use does not flood the events table. Orphans are recorded when a task id is
   claimed (`unknown_task`, `invalid_task_id`, `no_tmux_env`, `no_session`, pane/server pid/socket mismatch) or a known
   pane id mismatches (`launched_without_task_env`, `ambiguous_pane`, pid/socket mismatch). `hook.orphan` has no
   task/session columns; the claimed task is in the payload.
4. **Missing home / registry are never created** by the hook (it would create an empty registry and orphan everything).
   With a task id it prints one `agentctl hook: …` line on stderr (no log location exists); otherwise it is silent.
   Every other error goes to `logs/hook-errors.log` (0600, rotated to `.1` past 1 MiB).
5. Record-only events never upgrade confidence (an adopted `unknown` stays `unknown`/`none` until a state-setting hook).
6. `hook.received` payload: `{event, match, agent_session_id?, prompt_sha256?, source?, tool_name?,
   notification_type?, trigger?, reason?, error?}`; values are sanitized and ≤64 bytes. Details are ≤200 bytes.
7. `hooks-snippet` prints exactly the generated file (byte-identical) on stdout and a one-line merge hint on stderr.
8. `cmd/agentctl` imports `hookingest` (allowed) and not `adapter/claude`: `SettingsSnippet` lives in hookingest.

## Risks / follow-ups

- **Pre-existing flake, not mine (skeleton `internal/store/concurrency_test.go`):** `TestConcurrentWritersNoBusy`
  fails ~1 in 8 runs, also on a pristine export of `main` (2/15 with `-count=15`): `BEGIN IMMEDIATE` returns
  `SQLITE_BUSY` at once in a helper process instead of waiting out `busy_timeout` (likely WAL-index recovery or a
  stale snapshot when many processes open the DB). `scripts/doctor.sh` can therefore go red at random. The hook
  path guards itself: `hookingest/busy.go` retries a `RecordHook` that failed busy in < 100 ms (≤ 4 retries,
  25/50/75/100 ms backoff, within the budget); a busy that waited out the timeout is not retried. Suggest the store
  owner apply the same (or `PRAGMA`-level) mitigation in `withTx` for every writer (TUI, deliver).

- **Workspace trust**: per the hooks reference, interactive sessions hold back *all* settings-file hooks until the
  folder's trust dialog is accepted. A fresh `../<repo>-worktrees/<task>` may prompt for trust: no hooks fire until the
  user accepts, so the task reads `starting` → `unknown` after 30 s. m3 demo should check; maybe document "trust the
  worktrees parent once".
- **StopFailure** (API error ends the turn) fires *instead of* Stop: the session stays `working`. It is recorded but
  maps to no state (the table has no row). Consider `StopFailure → ready` (the prompt is back) in a later card.
- **SessionEnd default budget** is 1.5 s; our `timeout: 5` raises it, and the hook finishes in ~10 ms anyway.
- Measured (macOS, warm binary): valid payload ~10 ms; DB write-locked by another process 1.54 s (busy timeout
  1.5 s, then SQLITE_BUSY logged); the 1.8 s self-timeout is the backstop.
