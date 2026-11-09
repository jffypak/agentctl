# agentctl — Architecture & Spec (Phase 1)

> Spec from `agent-control-spec.md` (Draft v0.1). Generated 2026-11-06 via /tb:spec.
> Scope of this build: **Phase 1 only** (session manager). Phases 2–3 are filed
> as backlog cards (`m4-*`, `m5-*`) and are NOT built in this round.
> Module `github.com/jffypak/agentctl`, binary `agentctl`, Go 1.27.

## Vision

One popup that tells you which of your coding agents needs you, and lets you
answer it without hunting through tmux.

## Who / Wedge / Why now

- **User + job + bad day:** an engineer running 3–6 Claude Code sessions in
  parallel Git worktrees inside tmux while doing their own work. They come back
  from 30 minutes heads-down and cycle through every pane, only to find one agent
  has sat on a permission prompt for 25 minutes, one finished 20 minutes ago, and
  a prompt they typed landed in the wrong pane in the middle of a tool call.
- **Wedge:** a durable task↔worktree↔pane registry plus hook-driven agent state,
  rendered as an attention-sorted popup. Prompts queue when the agent is busy
  and are never blind-pasted. One key attaches to the pane, one key opens the diff.
- **Why now:** Claude Code's lifecycle hooks (`SessionStart`, `UserPromptSubmit`,
  `Notification`, `Stop`, `SessionEnd`, …) give structured agent state without
  screen-scraping. Worktrees make parallel agents cheap. The bottleneck has moved
  from agent throughput to human attention.

## Success metric

**Median time from opening the popup to the first action is under 60 s.** It is
computed from `ui.opened` → first `ui.action` events and printed by `agentctl stats`.
Secondary: *attaches per prompt sent*. If it falls, users trust the TUI prompt
path instead of jumping into panes.

## Scope

- **In (v0 = Phase 1):** Go + Bubble Tea v2 TUI (full-screen or tmux popup);
  launch Claude Code in a new worktree + tmux session; adopt an existing pane;
  hook-driven agent state with explicit `unknown`; attention-sorted list and
  detail view; recent output capture; attach/focus; safe prompt delivery-or-queue
  with visible delivery state; diff summary and full diff; SQLite registry that
  survives TUI restarts; stale or reused panes shown as `disconnected`.
- **Out (later):** Phase 2 task contracts, checks, the correction loop, skills,
  and merge approval (`m4-*`). Phase 3 workflow DAG, integration checkout, and
  locks (`m5-*`). Phase 4: more adapters, planner, learning edge. Also out:
  relaunching a disconnected agent, deleting worktrees, a daemon, Windows.
- **Never:** a new coding agent, a terminal emulator, replacing tmux/git/editor,
  auto-merge without approval, production deploys or destructive DB actions.

## User flows

- **Happy path (new task):** `prefix a` opens the popup (`display-popup -E agentctl`).
  `n` asks for name + goal. agentctl creates worktree `../<repo>-worktrees/<task>`
  on branch `agentctl/<task>`, starts tmux session `agentctl-<task>` running
  `claude --settings <hooks.json> "<goal>"`, and the agent shows as `starting`.
  The user presses `q` and goes back to work.
- **Happy path (check in):** reopen the popup. NEEDS YOU lists
  `auth-tests · awaiting input · permission prompt (4m)`. `↵` opens detail, then
  `↵` attaches (`switch-client` inside tmux, `tmux attach` outside). The user
  approves the prompt, detaches, and reopens.
- **Happy path (prompt):** select a `working` agent and press `p`, type, then `ctrl+s`.
  The message shows `queued · agent working`. On the agent's `Stop` hook it is
  delivered (`sent`), then marked `acknowledged` when `UserPromptSubmit` fires.
- **Unhappy: pane gone or reused.** The pane is killed, or the tmux server restarts
  and reuses `%N`. The identity triple no longer matches, so the task shows
  DISCONNECTED in NEEDS YOU. It is never re-attributed to another pane.
  Actions: inspect, diff, close.
- **Unhappy: delivery unconfirmed.** The message was pasted but no `UserPromptSubmit`
  arrived within 15 s, or agentctl crashed mid-delivery. The message becomes
  `unconfirmed` and appears in NEEDS YOU ("attach to check"). It is **never
  auto-retried**.
- **Unhappy: adopted pane without hooks.** State is `unknown` (confidence `none`).
  Prompts are always queued with the label "attach to deliver" and are never pasted.
- **Empty state:** "No agents yet — `n` new · `a` adopt a running claude pane",
  plus the one-line tmux binding hint.
- **Error states:** tmux missing, git missing, cwd not a git repo, `claude` not on
  PATH, branch or worktree already exists. Each gets a one-line actionable error
  that never crashes the TUI.

## Data model sketch

SQLite via `modernc.org/sqlite` (pure Go, no cgo), opened with WAL +
`busy_timeout=5000`, because short-lived `agentctl hook` processes write
concurrently with the TUI. Home: `$AGENTCTL_HOME`, else `$XDG_STATE_HOME/agentctl`,
else `~/.local/state/agentctl` → `agentctl.db`, `logs/`, `hooks/claude-settings.json`.
Config lives at `$XDG_CONFIG_HOME/agentctl/config.yaml` (optional).

| Table | Key fields |
|---|---|
| `schema_migrations` | `version` |
| `repos` | `id`, `root_path` (unique, realpath), `name` |
| `tasks` | `id` (slug PK), `repo_id`, `name`, `goal`, `state` (`running`/`closed`), `worktree_path`, `branch`, `base_ref`, `base_sha`, timestamps |
| `sessions` | `id`, `task_id`, `agent_type`, `origin` (`launched`/`adopted`), **identity:** `tmux_socket`, `tmux_server_pid`, `tmux_session_id`, `tmux_pane_id`, `pane_pid`; `agent_session_id` (from hooks); `agent_state`, `state_confidence` (`hook`/`pane`/`none`), `state_since`, `last_detail`, `ended_at` |
| `messages` | `id`, `task_id`, `session_id`, `body`, `body_sha256`, `state` (`queued`→`delivering`→`sent`→`acknowledged` \| `unconfirmed` \| `failed` \| `cancelled`), `ack_match` (`hash`/`timing`), `created_at`, `claimed_at`, `sent_at`, `acked_at`, `error` |
| `events` | `id` (autoinc), `ts`, `type`, `task_id`, `session_id`, `cause_id`, `payload` (JSON) — append-only |

The **entire Phase-1 schema is `migrations/0001_init.sql`**, written by the
skeleton. The migrator applies every *unapplied* version in ascending order (set
semantics, not "max+1"). Write transactions use `BEGIN IMMEDIATE`
(`_txlock=immediate`). `agentctl hook` opens with `busy_timeout=1500` so it stays
inside its 2 s budget. The DB file is mode 0600.

**Tunables** (`controller/tunables.go`, overridable in tests): `AckWindow 15s`,
`DeliverSettle 250ms`, `PasteEnterDelay 100ms`, `StartingTimeout 30s`,
`Poll 300ms`, `Reconcile 2s`, `HookBudget 1.8s`.

Prompt text the user types directly into a pane is stored as a **hash only**. No
transcript contents and no secrets go into the DB. Bulky captures go to `logs/` files.

## System architecture

```text
 tmux popup / terminal
 ┌──────────────────────────┐   poll events (id > last) 300ms
 │ agentctl  (TUI, bubbletea│──────────────┐  reconcile panes 2s
 │ v2) — presentation only  │              ▼
 └────────────┬─────────────┘     ┌──────────────────┐
              │ controller API    │  SQLite (WAL)    │◀─── agentctl hook  (spawned by Claude
              ▼                   │  registry+events │      Code per hook event; <2s, exit 0,
 ┌──────────────────────────┐     └──────────────────┘      fail-open, no stdout)
 │ internal/controller      │──────────▲                         │ on →ready with queue:
 │ NewTask Adopt Reconcile  │                                    ▼ spawn detached
 │ Send/Queue Drain Inspect │                          agentctl deliver --session N
 └──┬──────────┬────────┬───┘
    │          │        │
 ┌──▼───┐  ┌───▼───┐ ┌──▼────────────────┐
 │gitx  │  │ tmux  │ │adapter/claude     │  launch argv, settings JSON,
 │(CLI) │  │(CLI,  │ │(+ fakeagent in    │  hook→state map, deliver policy
 └──────┘  │socket)│ │ tests)            │
           └───────┘ └───────────────────┘
```

**No daemon in Phase 1** (resolves spec open question 2). Agents live in tmux, so
TUI exit never kills them. Background delivery comes from hook processes spawning
a detached `agentctl deliver`. Both the TUI tick and `deliver` call the same
idempotent `controller.Drain`.

### Packages

| Package | Responsibility |
|---|---|
| `cmd/agentctl` | entry; stdlib `flag` subcommands: *(none)*=TUI, `new`, `adopt`, `ls [--json]`, `show`, `diff`, `send`, `close`, `hook`, `deliver`, `stats`, `hooks-snippet`, `tmux-binding` |
| `cmd/fakeagent` | scripted Claude Code stand-in for tests (see contract below) |
| `internal/domain` | pure types, state enums, transitions, attention classification. **No I/O.** |
| `internal/store` | open/migrate SQLite; `Tasks`, `Sessions`, `Messages`, `Events` repositories; conditional-UPDATE claims |
| `internal/tmux` | tmux CLI wrapper; every call carries an explicit socket (`-L`/`-S`); pure arg builders + `Runner` interface |
| `internal/gitx` | git CLI wrapper: worktree add/list, diff `--numstat` vs base, untracked, conflict status |
| `internal/adapter` | `AgentAdapter` interface; `adapter/claude` implementation |
| `internal/hookingest` | `agentctl hook`: stdin JSON + env → observation → store; spawns `deliver` |
| `internal/controller` | use-cases; with `hookingest`, the **only** writers of task/session/message state |
| `internal/tui` | Bubble Tea v2 models; reads and acts via controller only |
| `internal/testenv` | `TempRepo`, `TmuxServer` (isolated `-L agentctl-test-<rand>`, `-f /dev/null`), `BuildFakeAgent`, `TempHome` |

Also: `internal/paths` (home/config resolution), `internal/archtest` (enforces
the invariants and the import rules below), `internal/pager`, `internal/config`
(`config.yaml`, `keys:` only in Phase 1), and `internal/deps/pin.go`
(`//go:build tools` blank imports so `go mod tidy` keeps every pin).

Pinned deps: `charm.land/bubbletea/v2 v2.1.1`, `charm.land/bubbles/v2 v2.2.1`,
`charm.land/lipgloss/v2 v2.0.6`, `modernc.org/sqlite v1.61.1`,
`go.yaml.in/yaml/v3 v3.0.6`, `golang.org/x/term v0.47.0` (fakeagent raw mode),
`github.com/charmbracelet/x/exp/teatest/v2` (tests, pseudo-version). We shell out
to `tmux` ≥3.3 and `git` ≥2.40, with no libgit2. No card adds a module after
the skeleton.

**Import graph:** `cmd → {controller, hookingest, tui}`, `tui → controller`,
`controller → {domain, store, tmux, gitx, adapter}`, and
`hookingest → {domain, store, adapter}`. `controller` and `hookingest` never
import each other. `hookingest` starts `deliver` through an injected `Spawner`
(`os.Executable() deliver --session N --after-pid <hook pid>`, `Setsid`, stdio
to `logs/deliver.log`).

### Agent adapter

```go
type AgentAdapter interface {
    Name() string
    Start(ctx context.Context, spec LaunchSpec) (Handle, error)   // tmux session in spec.Worktree
    Observe(ctx context.Context, h Handle) (Observation, error)   // pane liveness + hook state
    Deliver(ctx context.Context, h Handle, m Message) (Receipt, error) // ErrNotReady unless deliverable
    Stop(ctx context.Context, h Handle) error                       // only on explicit confirmed close
    ParseHook(payload []byte, env HookEnv) (HookObservation, error)
}
```

Adapters report observations. They never mark tasks verified (Phase 2) or approved.

**Launch.** `tmux new-session -d -P -F '#{session_id} #{pane_id} #{pane_pid}'
-s agentctl-<task> -c <worktree> -e AGENTCTL_TASK_ID=<task> -e AGENTCTL_HOME=<home>
-- claude --settings <home>/hooks/claude-settings.json "<goal>"`, then
`set-option -t <session> remain-on-exit on`. That keeps a crashed agent's pane
around so its exit status and final output can be read (state `failed`). Both
commands go in **one** tmux invocation (`… \; set-option -w remain-on-exit on`)
so an agent that exits instantly can't race it. The agent argv prefix comes from
`AGENTCTL_AGENT_CMD` (a JSON array, default `["claude"]`), followed by
`--settings <file> <goal>`. This is how tests substitute `fakeagent` or `sleep`.
The goal is passed as a single argv element and never through a shell.

`new` resolves the *main* repo with `git rev-parse --git-common-dir` (running it
inside a worktree registers against the main repo), records `base_ref` and
`base_sha`, and then runs `git worktree add -b agentctl/<task>
../<repo>-worktrees/<task> <base_sha>`. If tmux fails after the worktree exists,
the task is kept with session `failed` and the worktree is left in place. An
adopted task's base is `merge-base(HEAD, main checkout HEAD)`, falling back to
`HEAD`. All gitx reads run with `GIT_OPTIONAL_LOCKS=0`. Slugs are
`[a-z0-9][a-z0-9-]{0,39}`.

**tmux socket resolution:** `AGENTCTL_TMUX_SOCKET`, then the socket in `$TMUX`,
then the default server. It is stored as an absolute path and compared after
`EvalSymlinks` (macOS `/tmp` → `/private/tmp`).

**Hook settings** (generated, written atomically): one command hook per event,
`"<abs path of agentctl>" hook`, `timeout: 5`. The hook process gets the payload
on stdin plus inherited env `AGENTCTL_TASK_ID`, `AGENTCTL_HOME`, `TMUX`, and `TMUX_PANE`.

| `hook_event_name` | condition | → `agent_state` |
|---|---|---|
| `SessionStart` | `source == "startup"` on a session agentctl launched with a goal (the task row's `goal` ≠ `""`) | stays `starting`, detail `submitting goal` (the goal's `UserPromptSubmit` follows ~230 ms later; `ready` here would allow a paste into an agent about to work) |
| `SessionStart` | otherwise, `source` ≠ `compact` (no goal, adopted, `resume`, `clear`) | `ready` |
| `SessionStart` | `source == "compact"` | no change (auto-compact fires mid-turn; `ready` here would allow a paste into a working agent) |
| `UserPromptSubmit` | — | `working` (+ ack matching) |
| `PreToolUse` | `tool_name == "AskUserQuestion"` | `awaiting_input` |
| `PreToolUse` / `PostToolUse` / `PreCompact` | otherwise | `working` |
| `Notification` | `notification_type` ∈ {`permission_prompt`,`elicitation_dialog`} | `awaiting_input` |
| `Notification` | `idle_prompt` | `ready` |
| `PermissionRequest` | — | `awaiting_input` |
| `Stop` | — | `ready` |
| `StopFailure` | (an API error ended the turn; fires *instead of* `Stop`) | `awaiting_input`, detail `turn failed: <error>` (NEEDS YOU, and no auto-delivery into a failing session) |
| `SessionEnd` | — | `stopped` |
| anything else | — | recorded, no change |

`starting` with no hook within `StartingTimeout` → `unknown` / confidence
`none`. The same timeout covers a goal that is never submitted: a
goal-launched agent is still `starting` (confidence `hook`) after its
`SessionStart`, so it surfaces as `unknown` with detail `goal never
submitted: …`, not the folder-trust hint (a trust dialog comes before any
hook). The goal-hold is decided per hook from the persisted task row, so it
survives restarts; `starting` → `ready` therefore happens only on the goal
turn's `Stop`. `stopped`, `failed`, and `disconnected` are sticky: a later hook never
revives them. Every state write, from hooks or reconcile, goes through
`store.Sessions.Transition(id, to, confidence, detail, source)`. That is a
conditional UPDATE guarded by `domain.CanTransition`, and it appends
`agent.state_changed` in the same transaction.

**Hook stdout must be empty.** Claude Code feeds `SessionStart` and
`UserPromptSubmit` hook stdout into the model's context, so any stray output is
a prompt injection.

**Send.** Validate the body (non-empty, ≤64 KiB, no NUL), insert it as
`queued`, then `Drain`. Sending to a `stopped`, `failed`, or `disconnected` agent
is **rejected** and nothing is stored. Sending to `unknown` queues with the
label "attach to deliver" until the user cancels it.

**Delivery.** This is the only path that writes into a pane:
1. Atomically claim: `UPDATE messages SET state='delivering' WHERE id=? AND state='queued'`,
   inside `BEGIN IMMEDIATE`. The claim also requires that no message on the
   session has `sent_at ≥ sessions.state_since`. That rule gives one delivery
   per ready transition, even with concurrent drainers.
2. Re-verify at delivery time: `agent_state=='ready'`, `state_confidence=='hook'`,
   the identity triple matches the live pane, and the pane is not dead.
   Otherwise revert to `queued`.
3. `tmux load-buffer -b agentctl-msg-<id> -` (stdin), then `paste-buffer -p -d -t <pane>`
   (bracketed paste), then `send-keys -t <pane> Enter`, then mark the message `sent`.
4. On `UserPromptSubmit` for that session: if the body hash matches → `acknowledged`
   (`ack_match=hash`). Otherwise the first submit within 15 s of `sent` → `acknowledged`
   (`ack_match=timing`). No ack within 15 s → `unconfirmed`.
5. Deliver one message per ready transition. The next one waits for the next `Stop`.
   An `unconfirmed` message does not block the queue, and it is never re-pasted.
   A late hash match may upgrade it to `acknowledged`.

The hook-spawned `deliver` waits for the hook pid to exit, waits
`DeliverSettle`, runs `Drain`, then waits up to `AckWindow`. It always exits 0.

**Lost-hook safety.** A hook whose DB write fails (for example SQLITE_BUSY
past its budget) writes a marker file instead
(`<home>/hooks/lost/<session-or-pane-key>`). While the marker is newer than the
session's `state_since`, the session is not deliverable ("state stale —
attach to deliver"). Reconcile downgrades it to `unknown`/`none`, and the next
recorded hook clears it. A lost `UserPromptSubmit` therefore can never lead to
a paste into a working agent.

**Identity and reconciliation.** A session is bound to (tmux socket, server pid,
pane id, pane pid), captured at launch or adoption. Reconcile runs
`list-panes -a -F` on the session's socket. A missing pane, a different `pane_pid`,
or a different server pid → `disconnected`. A dead pane → `failed` (non-zero
`pane_dead_status`) or `stopped`. Hooks whose `$TMUX_PANE`/`$TMUX` don't match the
session's identity are recorded as orphan events and change nothing. A hook with
no `AGENTCTL_TASK_ID` (an adopted pane whose user merged `agentctl
hooks-snippet` into their own settings) is matched by pane identity, which
upgrades that session's confidence to `hook`.

**Identity is verified before capture, attach, and delivery**, all through one
`controller.liveSession(sess) (domain.Liveness, tmux.Pane)`. A reused `%N` must
never leak another agent's output or focus. Captured pane text and hook-supplied
text pass through `domain.SanitizeTerminalText`, which strips CSI and OSC
(including OSC 52) and other control sequences before anything is displayed.

### fakeagent contract (tests only)

Accepts `--settings <file>` and an optional positional initial prompt. It runs
the command hooks found in that file exactly as Claude Code would: JSON on stdin
with `session_id`, `hook_event_name`, `cwd`, and event fields, inheriting env. It
puts the tty in raw mode, enables bracketed paste (`\e[?2004h`), and prints a `> `
prompt. On submit it fires `UserPromptSubmit`, "works" for `FAKEAGENT_WORK_MS`
(default 300) while firing `PreToolUse`/`PostToolUse` and printing progress, then
fires `Stop`. Directives inside a prompt: `#ask` fires `Notification
permission_prompt` and waits for `y`; `#exit N` exits with code N; `#sleep MS`.
With `FAKEAGENT_PASTE_PLACEHOLDER=1`, pasted input is reported to
`UserPromptSubmit` as `[Pasted text #1]`, which exercises timing acks. It flips
to `ready` only after the `Stop` hook command returns, and fires `SessionEnd` on exit.
`FAKEAGENT_GOAL_DELAY_MS` waits that long between `SessionStart` and submitting
the initial prompt (real Claude: ~230 ms), staying `starting` meanwhile.
Every received input is appended as JSONL to `$FAKEAGENT_LOG`: `{ts, text,
state_at_receipt}`. Tests use this to assert that **nothing arrived while
`working`**.

## Invariants

These are used by the acceptance pass. A violation is a failing review.

1. **TUI is presentation only.** It never opens the DB for writing. All state
   changes go through `controller` or `hookingest`.
2. **Agent lifetime ≠ agentctl lifetime.** Exiting the TUI, `hook`, or `deliver`
   never kills a tmux session. Only an explicit, confirmed close-with-stop does.
3. **No blind injection.** Nothing writes into a pane except `controller.Deliver`,
   and only under the five delivery rules above. In particular it **never pastes
   into `awaiting_input`**, because text in a permission dialog can approve an
   action.
4. **At-most-once delivery.** A message is claimed with a conditional UPDATE.
   After a crash, `delivering` or `sent` without an ack becomes `unconfirmed` and
   is shown to the user. It is never redelivered automatically.
5. **Explicit identity.** A task is never inferred from cwd or command name.
   Pane-id reuse is detected through the identity triple and rendered as
   `disconnected`. Capture, attach, and delivery all verify identity first.
6. **Unknown is honest.** Missing or ambiguous hooks render as `unknown`, never as
   `ready`, idle, or done. A failed or unknown state never renders as success.
7. **Hooks fail open.** `agentctl hook` always exits 0, writes nothing to stdout,
   finishes in under 2 s (self-timeout), and logs errors to `logs/hook-errors.log`.
8. **Tests are hermetic.** Every tmux call goes through `internal/tmux` with an
   explicit socket. No test touches the user's tmux server, `~/.claude`, or real
   `$HOME` state.
9. **No destructive git.** Phase 1 never deletes worktrees or branches and never
   runs `reset --hard` or force operations. Close only marks a task closed.
10. **Domain is pure.** `internal/domain` imports no I/O packages. State
    transitions and attention classification are table-tested there.

## Attention classification (`internal/domain`)

| Section | Rule |
|---|---|
| **NEEDS YOU** | `awaiting_input`, `disconnected`, `failed`, or any message `unconfirmed`/`failed`, or a queued message on an `unknown` agent |
| **IDLE** | `ready`: turn finished, waiting for the next instruction or review. Also `stopped`, labelled **exited** (never worded as success) |
| **RUNNING** | `starting`, `working` |
| **NEEDS YOU** (also) | `unknown` on a **launched** agent → "silent since launch · attach to check": it never sent a hook (Claude's folder-trust or login prompt, early hang) |
| **UNKNOWN** | `unknown` (adopted without hooks): muted |
| *(hidden)* | tasks in state `closed`; shown with the `/` filter `closed` |

Within a section, rows are ordered by `state_since`, oldest first. The header
reads `agents / <repo> · N running · M need you`. Colors are semantic only
(NEEDS YOU = accent, UNKNOWN = faint) and adapt to light and dark terminals.

## Instrumentation

Events: `ui.opened`, `ui.closed`, `ui.action{action,task_id}` (attach, prompt,
diff, logs, new, adopt, close; all carry a `ui_run_id`),
`task.{created,closed}`, `session.{started,adopted}`,
`agent.state_changed{from,to,source}`, `session.disconnected{reason}`,
`message.{queued,claimed,sent,acknowledged,unconfirmed,cancelled}` (with a
`cause_id` pointing to the triggering hook event), `hook.received{event}`, and
`hook.orphan`. Event-type constants live in `internal/domain`. The leading
indicator, open → first action, comes from `agentctl stats`.

## Testing

- **Unit:** domain transitions and attention, hook→state mapping, delivery
  preconditions, message claim races, settings generation, tmux and git arg builders.
- **Integration:** a real tmux server on an isolated socket, temp git repos,
  `fakeagent` with real hooks into a temp `AGENTCTL_HOME`. Skip cleanly when
  tmux or git is absent.
- **TUI:** model update tests plus `teatest` golden snapshots at 80×24 and 120×40
  for empty, mixed-attention, detail, and composer states.
- **E2E / Phase 1 acceptance:** `internal/e2e`, one test per acceptance criterion
  (see `m3-acceptance-e2e`).
- **Real agent:** `scripts/demo.sh` starts two real `claude` sessions in a
  throwaway repo, run once by hand. It is not part of `go test`.
- **Gate:** `scripts/check.sh`. It fails if `gofmt -l` is non-empty, `go vet ./...`
  fails, `go test -race ./...` fails, or `go mod tidy` would change
  go.mod or go.sum. `scripts/doctor.sh` just runs `check.sh`; tinkerbell's
  acceptance pass runs it in every worktree.
- **Hermetic tests:** testenv clears `TMUX` and `TMUX_PANE` for child processes,
  uses one short `-L agentctl-test-<rand>` socket per test (not `-S` under
  `t.TempDir()`, which hits the macOS path-length limit), and runs `kill-server`
  in `t.Cleanup`. Parallel tests don't use `t.Setenv`; env is passed explicitly
  through `testenv.Env()`. Waits use wait-for helpers, never fixed sleeps.

## Code conventions & file ownership (parallel-merge rules)

Phase-1 cards run in parallel worktrees and are merged by the director. These
rules keep merges trivial:

1. **One owner per file.** Each card's description lists the files it owns. The
   skeleton (and tui-inbox for `internal/tui`) creates **stub files with their
   final signatures** for later cards. The owning card rewrites a stub whole and
   never edits files outside its list. A needed signature change goes in a note
   in `docs/notes/<issue-id>.md`. It does not go in someone else's file.
2. **Per-concern files.** `cmd/agentctl` has one `cmd_<name>.go` per subcommand,
   registered in a fixed table in `commands.go` (skeleton-owned, complete from
   day one). `internal/controller` has one file per use case;
   `internal/adapter/claude` one file per interface method; tmux and gitx one
   file per command family; store methods go in per-card files
   `<entity>_<concern>.go`.
3. **No new modules or migrations** after the skeleton. If one becomes
   unavoidable, use the reserved migration ranges: hook-state 0100s,
   pane-identity 0200s, inspect 0300s, tui-inbox 0400s, prompt-queue 0500s,
   tui-manage 0600s, e2e 0700s, m4+ 1000+. Say so in the final report so the
   director can re-run `go mod tidy`.
4. **Shared write paths:** `sessions.agent_state` changes only through
   `Sessions.Transition`, and message state only through conditional UPDATEs in
   `store/messages.go`.
5. **Docs:** subagents don't edit `docs/ARCHITECTURE.md` or `docs/kanban.md`
   (the director regenerates that). Findings go in `docs/notes/<issue-id>.md`.
6. **Archtest rules** (written by the skeleton; a rule for a package that
   doesn't exist yet passes vacuously; each rule has a negative self-test):
   - `domain` imports only an allowlist of pure stdlib packages.
   - `tui` does not import `store`, `tmux`, `gitx`, `hookingest`, `database/sql`, or sqlite.
   - `controller` and `hookingest` don't import each other.
   - `exec.Command("tmux"` appears only in `internal/tmux`, and `"git"` only in
     `internal/gitx` and `internal/testenv`.
   - paste-buffer and send-keys builders are used only from `tmux/buffer.go`,
     `claude/deliver.go`, testenv, e2e, and `_test.go` files.
   - kill-session and kill-pane appear only in `tmux/kill.go` and `claude/stop.go`;
     kill-server only in testenv.
   - gitx never contains `reset --hard`, `worktree remove`, `branch -D`,
     `--force`, `clean -f`, or `push`.
   - gitx reads against a worktree use plumbing (`diff-tree`, `diff-index`,
     `diff-files`) or `status`, never porcelain `git diff`. Porcelain diff
     rewrites `.git/index` (refresh) even with `GIT_OPTIONAL_LOCKS=0`
     (m1-inspect finding).
   - tmux `pane_dead_signal` may be a name (`term`), not a number. Unknown
     names map to a non-zero sentinel, never 0.
7. **Environment:** the user's global gitignore ignores **all dotfiles** and any
   `go/` directory. Don't add dotfile config (no `.golangci.yml`), and don't
   name a package directory `go`. Commit all work on the worktree branch before
   reporting; uncommitted work is lost at merge.

## Non-goals

Verification checks, retries, approvals, skills, workflows, graph view, planner,
learning edge, other agent CLIs, remote or multi-user use, a background daemon,
relaunching disconnected agents, worktree cleanup, and deep config schemas.

## Open questions

**Answered by the real two-Claude demo** (2026-11-09, Claude Code 2.1.319,
tmux 3.5a, `DEMO_AGENT=claude scripts/demo.sh`, two runs):

- **Hook reliability: yes.** Every turn produced `UserPromptSubmit` → tool
  hooks → `Stop`, no orphan hooks, no `hook-errors.log` entries. One gap: the
  `SessionStart` → `ready` mapping is premature when a goal is passed on argv
  (see the session-start race below).
- **`--settings` merge: additive.** agentctl's hooks and the user's own plugin
  hooks (ECC session hooks) both ran in the same agent session. `--settings`
  does not replace user or project hooks.
- **Large paste ack: placeholder, so `timing`.** A 1023-byte bracketed paste
  was acknowledged by `timing`; Claude collapses it to a "paste again to expand"
  placeholder, so `UserPromptSubmit.prompt` doesn't carry the text. A 30-byte
  prompt matched by `hash`. Keep the timing fallback; it is the normal path for
  large prompts, not an edge case.
- **Popup `switch-client`: works.** `prefix a` → select beta → Enter landed
  the user on `agentctl-beta`'s pane after the popup closed.
- **Field names seen:** `SessionStart.source = startup`,
  `Notification.notification_type = idle_prompt`. No `permission_prompt` or
  `PermissionRequest` was exercised (neither agent hit a permission prompt), so
  the NEEDS YOU permission path is still only proven against fakeagent.
- **Claude `pane_current_command` is the version string** (`2.1.319`), not
  `claude`: the binary is a symlink into a versions directory. The default
  adopt filter (`DefaultAdoptCommands = ["claude"]`) therefore matches no real
  Claude pane. Tracked in `m3-adopt-claude-match`.
- **Folder trust blocks every first run.** Each worktree is a new path, so
  Claude shows its trust dialog before any hook fires. The agent sits in
  `starting` and surfaces as `unknown` only after `StartingTimeout` (30 s). We
  still never auto-accept; tracked in `m3-folder-trust-detect`.
- **Session-start race.** With a goal on argv, `SessionStart` arrived ~230 ms
  before the goal's `UserPromptSubmit`, and mapping it to `ready` made the
  agent look deliverable in that window. Fixed in `m3-goal-start-race`: on a
  goal-launched session `SessionStart(startup)` keeps `starting` ("submitting
  goal"); the goal turn's `Stop` is the first `ready` (see the hook table).

Still open:

- Permission prompt → NEEDS YOU with real Claude (not exercised by the demo).
  Owner: whoever next runs the demo with a goal that triggers a prompt.
- Multiple agents per task or per pane are out of Phase 1: one session per task,
  many tasks per repo. Owner: TBD (m4).
- Enforced sandboxing and command permissions. Owner: TBD (m4).

## Milestones

| Slug | Outcome | Phase |
|---|---|---|
| `m1-see-agents` | Launch or adopt agents from the CLI and see hook-driven, restart-safe state, output, and diffs | 1 |
| `m2-direct-agents` | Attention-first TUI with attach, safe prompt queue, diff and log views | 1 |
| `m3-phase1-acceptance` | Every Phase 1 acceptance criterion proven by an e2e test, plus the real two-agent demo | 1 |
| `m4-verified-tasks` | Phase 2: task contracts, checks, correction loop, inbox, skills, approval (**backlog, not built**) | 2 |
| `m5-workflow-graph` | Phase 3: declarative DAG, integration checkout, locks, graph view (**backlog, not built**) | 3 |

**Dispatch waves** (dependency-honest; cards in one wave own disjoint files):

```text
W0  m1-walking-skeleton
W1  m1-hook-state ∥ m1-pane-identity ∥ m1-inspect ∥ m2-tui-inbox
W2  m2-prompt-queue (hook-state, tui-inbox) ∥ m2-tui-manage (tui-inbox, inspect, pane-identity)
W3  m3-acceptance-e2e
```

Critical path: skeleton → max(hook-state, tui-inbox) → prompt-queue → e2e.
