# Codex support — design

Status: draft for review · 2026-11-12

## Intent

The user runs Codex CLI side by side with Claude Code, in tmux, on the same
repos. Today agentctl only understands Claude. A Codex pane can be adopted,
but it stays `unknown` forever and prompts sent from the popup never deliver
(they queue as "attach to deliver"). The user hit exactly that before.

Goal: a Codex agent in agentctl behaves like a Claude agent. It shows the
right attention section (NEEDS YOU / IDLE / RUNNING / UNKNOWN), and prompts
sent from the popup or `agentctl send` are delivered when Codex is ready.

The user wants both ways in:

1. **Launch:** `agentctl new <name> --agent codex --goal "…"`. It gets a
   worktree and tmux session, like Claude today.
2. **Bring your own pane:** `agentctl codex [--name x] [codex args…]`. It runs
   Codex in the current tmux pane and registers it automatically. This
   replaces "start codex, then `agentctl adopt`" for Codex.

## What we learned (probes run 2026-11-12, Codex CLI 0.167.2)

Probe scripts: `scratchpad/codex-probe/run.sh`, `run-daemon.sh`,
`manual-setup.sh` + `manual-finish.sh` (not committed; the findings are here).

- **Codex has command hooks, much like Claude's.** Events: `SessionStart`
  (`source`: startup/resume/clear/compact/fork), `SessionEnd`,
  `UserPromptSubmit` (`prompt`, `turn_id`), `PreToolUse`, `PostToolUse`,
  `PermissionRequest` (`tool_name`, `tool_input`), `PreCompact`,
  `PostCompact`, `SubagentStart`, `SubagentStop`, `Stop`
  (`last_assistant_message`), `Interrupt`, `PreModelRequest`,
  `PostModelRequest`. Payload is one JSON object on stdin with
  `session_id`, `cwd`, `hook_event_name`, `model`, `permission_mode`,
  `transcript_path`. There is **no** `Notification` and **no** `StopFailure`.
  Source: https://learn.chatgpt.com/docs/hooks, plus observed payloads.
- **Hooks can be passed on the command line**, verified:
  `codex -c 'hooks.Stop=[{hooks=[{type="command",command="/abs/hook.sh"}]}]'`.
  Empty hook stdout was accepted for `SessionStart`, `UserPromptSubmit`, `Stop`.
- **Two run modes.** By default `codex` uses a shared background service
  (`codex app-server` daemon). Any `-c`, `--enable`, `--disable`, `--search`
  or `--dangerously-bypass-hook-trust` forces **embedded mode**, and Codex
  shows the warning "Running without the shared background server: … requires
  embedded mode."
- **Embedded mode: hooks know their pane.** The hook's parent is the `codex`
  process in the pane. It inherits `TMUX` and the correct `TMUX_PANE`.
- **Daemon mode: hooks report the wrong pane.** The hook's parent is
  `codex app-server`, and `TMUX_PANE` is the pane where the daemon first
  started (`%8`, long gone), for every Codex session. Daemon mode also runs a
  hidden title-generation session (model `gpt-5.6-luna`, `transcript_path:
  null`) that fires its own `SessionStart` / `UserPromptSubmit` / `Stop`.
  The title session was **not** seen in embedded mode.
- **`SessionStart` fires late.** It fires at the first prompt, not when the
  TUI opens. A Codex started without a prompt fires no hook until the user
  types something.
- **Hook trust.** New or changed hooks are skipped until the user trusts them.
  At startup Codex shows a screen with "Review hooks", "Trust all and
  continue", and "Continue without trusting (hooks won't run)". Trust is
  stored per hook, keyed by a hash of the hook's command.
- No folder-trust dialog appeared for untrusted scratch folders (non-git). It
  is unknown whether one appears in a new git worktree.

## Decisions (made with the user)

- **Codex is supported only in embedded mode.** agentctl always starts Codex
  itself (`new --agent codex` or the `agentctl codex` wrapper), so it can pass
  `-c` hooks. A Codex started normally (daemon mode) cannot be tracked, and
  agentctl does not try.
- **Wrapper, not alias:** `agentctl codex` is the way to run your own Codex pane.
- **The wrapper registers the pane automatically.** No separate `adopt` step.
- **Never answer trust prompts for the user.** The hook-trust screen shows
  under NEEDS YOU as `hook trust: attach to answer`, the same pattern as
  Claude's folder trust. agentctl never sends keys to it and never passes
  `--dangerously-bypass-hook-trust`.
- **Delivery is tmux paste**, the same path as Claude. Not `codex queue`
  (it needs the daemon).
- **New INITIALSTATE section** for every agent (Claude too) that has not sent
  its first hook event. Exception the user chose: a launched agent still
  silent after `StartingTimeout` stays in NEEDS YOU. A Codex waiting for its
  first prompt is expected-silent and stays in INITIALSTATE.

Calls made by the designer (not the user), from the builder check:
`agentctl codex` runs Codex as a child and records its exit (not `exec`);
wrapper delivery checks Codex is still the pane's foreground process;
Codex `SessionStart` never sets `ready`; Codex `SessionEnd` is detail-only
like Claude's; `-c` hooks may override the user's own Codex hooks (accepted,
documented).

## Goals

- `agentctl new --agent codex` launches Codex with agentctl hooks in a
  worktree + tmux session; state and delivery work as for Claude.
- `agentctl codex` runs Codex in the current pane with hooks and registers it.
- Codex hooks map to agent states (table below). NEEDS YOU on permission
  prompts. IDLE after `Stop`. Prompts deliver only on `ready` + hook confidence.
- Claude behavior is unchanged. Claude stays the default agent.
- All ten invariants in `docs/ARCHITECTURE.md` hold for Codex sessions.

## Non-goals

- Tracking a Codex started without agentctl (daemon mode). `agentctl adopt`
  of a Codex pane stays possible but stays `unknown`; it should point the user
  to `agentctl codex` (see "Adopt").
- `codex queue` or the app-server API as a delivery path.
- Codex's own `--worktree` feature.
- Other agents (OpenCode etc.). The adapter work should make them easier, but
  they are not part of this.
- Editing the user's `~/.codex/config.toml` or `~/.codex/hooks.json`.

## Anti-goals (a failure even if it "works")

- Guessing a Codex pane from cwd, process name, or timing. The user runs Claude
  and Codex in the same folder (`~/Code/arize`); a guess would attribute one
  agent's state to another. (Invariant 5.)
- Trusting a hook event that ran inside `codex app-server`. Its `TMUX_PANE`
  looks valid but is wrong.
- Auto-trusting hooks or answering any Codex dialog.
- Pasting into Codex while a permission prompt or the hook-trust screen is up.
- A Codex agent shown `ready`/IDLE when agentctl has no hook proving it.

## Design

### Adapter registry

`controller.Controller` holds one `agent adapter.AgentAdapter` today
(`controller.go:57,109`), and `hookingest` calls `claude.*` directly
(`hookingest.go:193,287`, `resolve.go:56,98,147`, `goal.go:20`). The
`sessions.agent_type` column already exists.

Change this to a small registry keyed by agent type (`"claude"`, `"codex"`).
Every per-session operation (Observe, Deliver, Stop, ParseHook, goal hold,
pane-text dialogs) picks the adapter by the session's `agent_type`. Launch
picks it by the `--agent` flag (default `claude`).

Pieces that are not agent-specific today but live in `adapter/claude` (for
example `PaneFromEnv`, `HookPane.Matches`, `WithPasteEnterDelay`, the tmux
session-name prefix) should move to a shared place, or be reused from the
Codex adapter. Builder's call how.

### Codex adapter (`internal/adapter/codex`)

**Launch argv.** `<codex cmd> -c 'hooks.<Event>=[{hooks=[{type="command",command="<HookCommand>",timeout=5}]}]' … [goal]`
for each subscribed event. Each `-c` value is one argv element; no shell.
The goal is one positional element, as for Claude. The codex command prefix
comes from an env var like `AGENTCTL_AGENT_CMD` (builder's call on the name;
default `["codex"]`).

**Hook command.** `'<abs agentctl>' hook --agent codex`. It must be
byte-stable across launches so the user's one-time trust keeps working.

**Subscribed events:** `SessionStart`, `UserPromptSubmit`, `PreToolUse`,
`PostToolUse`, `PermissionRequest`, `PreCompact`, `Stop`, `Interrupt`,
`SessionEnd`.

**Hook → state.**

| Codex event | condition | → `agent_state` |
|---|---|---|
| `SessionStart` | any `source` | **no change** (recorded; detail `submitting goal` while `starting` with a goal). Codex fires it just before the first `UserPromptSubmit`, so `ready` here would let a queued message be pasted into an agent about to work — the race m3-goal-start-race fixed for Claude. This also covers `agentctl codex "<prompt>"`, whose positional prompt is not `task.Goal`. |
| `UserPromptSubmit` | — | `working` (+ ack matching on `prompt`) |
| `PreToolUse` / `PostToolUse` / `PreCompact` | — | `working` |
| `PermissionRequest` | — | `awaiting_input` (NEEDS YOU) |
| `Stop` | — | `ready` (the only way a Codex session becomes `ready`) |
| `Interrupt` | — | `ready` — **verify** (see open questions) |
| `SessionEnd` | — | detail only, no state change — same as Claude (`claude/hook.go` `mapHook`): `/clear` and `/resume` may fire it in a process that keeps running, and terminal states are sticky. Exit is detected from the dead pane (launched) or by the wrapper (below). |
| anything else | — | recorded, no change |

**Daemon guard (required).** Before resolving, `agentctl hook --agent codex`
checks its parent process. If the parent is `codex app-server` (daemon mode),
the hook records an orphan event (reason e.g. `codex_daemon`) and changes
nothing. This must hold even though agentctl only launches embedded Codex,
because the same hook command can be picked up by a daemon session (for
example if the user copies it into `~/.codex/hooks.json`).

**Session-id guard.** Bind the Codex `session_id` seen on the session's first
accepted hook. Later hooks from a different `session_id` in the same pane are
recorded but change nothing — unless they are a legitimate new session
(`SessionStart` with `source` clear/resume/fork, which rebinds). This protects
against hidden helper sessions like the daemon's title generator. Builder:
check whether Claude's ingest already has an equivalent and reuse it.

**Hook stdout stays empty** (invariant 7). Codex feeds `SessionStart` /
`UserPromptSubmit` stdout to the model, as Claude does.

**Delivery.** Same rules and same tmux bracketed paste + Enter as Claude
(`claude/deliver.go`). Ack matching uses `UserPromptSubmit.prompt`.

**Dialogs (pane text).** Extend the folder-trust pane-text check to be
per-agent. For Codex, the hook-trust screen ("Review hooks" / "Trust all and
continue" / "Continue without trusting") → `unknown` / `none`, detail
`hook trust: attach to answer`. Same rules as Claude's folder trust: only
while `starting` with no hook, identity-checked capture, never send keys. If
a Codex folder-trust dialog exists for new worktrees, handle it the same way.

**No hook before the first prompt.** Because `SessionStart` fires late, a
Codex with no goal (wrapper without a positional prompt, or `new --agent
codex` without `--goal`) gets no hook until the user types. That silence is
expected, so it is shown in the new **INITIALSTATE** section (below) with
detail `waiting for first prompt · attach to start`, not in NEEDS YOU, and it
stays there past `StartingTimeout`. Messages sent to it queue
("attach to deliver"), which moves it to NEEDS YOU like any queued message on
a session with no hook. A Codex launched with a goal (or a wrapper given a
positional prompt) should fire hooks at once; if it is still silent after
`StartingTimeout`, it goes to NEEDS YOU like a silent Claude launch.

### `agentctl codex` (wrapper)

`agentctl codex [--name <name>] [--] [codex args…]`

1. Require tmux (`$TMUX` and `$TMUX_PANE` set); otherwise exit with a clear error.
2. Register the current pane: create a task for the current repo (same repo
   resolution and base rules as adopt) with `agent_type = codex`. Identity is
   the current pane (socket, server pid, pane id, pane pid). Name: `--name`,
   or a default derived from the folder (e.g. `<dir>-codex`, with a numeric
   suffix on collision). Builder's call on the exact default.
3. Start Codex as a **child** (fork and wait, not `exec`) with the hook `-c`
   flags, the user's extra args, and `AGENTCTL_TASK_ID` + `AGENTCTL_HOME` in
   its env, so hooks resolve through the stronger task-env path
   (`MatchTaskEnv`). The wrapper passes the terminal through untouched
   (stdin/stdout/stderr, signals such as Ctrl-C/Ctrl-Z, window size) and exits
   with Codex's exit code.
4. When Codex exits, the wrapper records the session end itself: `stopped`
   on exit 0, `failed` otherwise (through the controller, so the normal
   transition rules apply). This is needed because the pane outlives Codex:
   the pane's pid is the user's shell, so reconcile sees a live pane and can't
   tell Codex is gone.
5. If the pane is already registered to a task whose session is
   stopped/failed, start a new session on that task instead of failing.
   If that session is still live, refuse with a clear message.

**Shell-paste guard (required).** For wrapper sessions, delivery also checks,
right before the paste, that the pane's foreground process is still Codex
(`pane_current_command`, matched like adopt matches Claude). If not, revert to
`queued` and mark the session not deliverable. This covers the case where the
wrapper was killed (e.g. SIGKILL) and could not record the exit. Without it, a
queued message would be pasted into the user's shell and run as a command
(invariant 3).

Origin: a new origin value (e.g. `wrapped`). It **starts as `starting`**
(like a launched session, so the hook-trust pane-text check runs and the
trust screen reaches NEEDS YOU), but follows **adopted stop rules**:
`close --stop` refuses, because the pane is the user's shell. The TUI detail
view shows it as Codex.

**Known limit (my call, verify):** a `-c hooks.<Event>=…` override may replace
the user's own Codex hooks for that event in that session. The user has no
Codex hooks today (`~/.codex/config.toml` has no `[hooks]`, no
`~/.codex/hooks.json`). Check whether `-c` replaces or merges; if it
replaces, document it in the README. Do not merge the user's config into the
`-c` value.

The user's own args are passed through unchanged. If the user passes
`--dangerously-bypass-hook-trust`, agentctl does not block it; that's their
choice. agentctl itself never adds it.

### Adopt

`agentctl adopt` keeps working. For a pane whose foreground process is Codex,
the listing and the adopt result should say that it can't be tracked and
suggest restarting it with `agentctl codex`. Builder's call on wording, and on
whether to list Codex panes at all by default.

### New attention section: INITIALSTATE (user's decision, applies to all agents)

A new section for **any agent that has not sent its first hook event yet**,
Claude or Codex. Display order:
NEEDS YOU, IDLE, RUNNING, **INITIALSTATE**, UNKNOWN (then hidden closed).
The section title is literally `INITIALSTATE` (the user's name for it).

| Session with no hook event received yet | Today | New |
|---|---|---|
| Launched, still inside `StartingTimeout` | RUNNING (`starting`) | INITIALSTATE |
| Launched, silent after `StartingTimeout` (Claude login screen, hang) | NEEDS YOU, "silent since launch · attach to check" | **unchanged: NEEDS YOU** (user's decision) |
| Codex (launched or wrapper) with no goal/prompt, any age | — | INITIALSTATE, `waiting for first prompt · attach to start` (expected silence, see above) |
| Adopted, no hooks installed | UNKNOWN (muted) | INITIALSTATE |

Rules that still win over INITIALSTATE (they come first in
`domain.ClassifyAttention`): a detected folder-trust / hook-trust screen
→ NEEDS YOU; any queued message on a session with no hook → NEEDS YOU
("queued · attach to deliver"); unconfirmed/failed messages → NEEDS YOU.

After this, UNKNOWN holds only sessions that *did* send hooks but whose state
agentctl lost (lost-hook downgrade, "state stale"). "Has sent its first event"
means at least one `hook.received` matched to the session; how that is stored
or derived (e.g. a flag or the session's confidence history) is the builder's
call. Classification stays pure in `internal/domain` (invariant 10) and gets
table tests. Update the attention table in `docs/ARCHITECTURE.md`. Header
counts (`N running · M need you`) do not count INITIALSTATE as running.
INITIALSTATE rows are muted like UNKNOWN unless the builder finds a reason not to.

### TUI

The list row should show which agent it is when more than one agent type is
present (a short `codex` tag is enough). Builder's call on exact rendering.
The popup's `n` (new) flow gets a way to pick the agent; default Claude.

## Testing

- `fakeagent` gets a Codex mode: accept `-c hooks.<Event>=…` flags, run them
  with Codex-shaped payloads (including `turn_id`, `permission_mode`,
  late `SessionStart`), and print the hook-trust screen on demand.
- Table tests for the Codex hook → state mapping in the adapter.
- Tests for both guards: a hook whose parent is `codex app-server` records an
  orphan and changes nothing; a hook from a different `session_id` changes
  nothing.
- End-to-end on the test tmux socket: `new --agent codex` → goal → `Stop` →
  `send` delivers; permission → NEEDS YOU and no paste; `agentctl codex` in a
  pane registers and tracks.
- Invariant 8: no test touches `~/.codex`, the user's tmux server, or a real
  Codex daemon.
- One manual check with real Codex (like the Claude two-agent demo):
  trust screen surfaces in NEEDS YOU, trust once, second launch has no trust
  screen, permission prompt → NEEDS YOU, prompt delivery + ack.

## Open questions for the builder to verify against real Codex

1. **Do `-c` hooks get persistent trust?** The trust record is keyed by the
   hook's command hash. If hooks passed with `-c` can be trusted once and stay
   trusted, nothing else is needed. If Codex asks again on every launch, the
   fallback is an explicit, opt-in command that writes the same hook to
   `~/.codex/hooks.json` (daemon sessions that pick it up are ignored by the
   daemon guard). Do not build the fallback unless the check fails.
2. **Does `PermissionRequest` fire for prompts the user actually sees?** The
   user's config has `approvals_reviewer = "auto_review"`. Check that a
   permission prompt shown to the user fires `PermissionRequest`, and what
   fires when the user approves (`PostToolUse` → `working` is assumed).
3. **`Interrupt` vs `Stop`.** When the user presses Esc mid-turn, does `Stop`
   also fire? If yes, map `Interrupt` to "no change". If not, `ready`.
4. **Failed turns.** With no `StopFailure`, what fires when a model request
   fails? (`PostModelRequest` has `status` / `error`.) Minimum: don't show
   `ready` for a failed turn if Codex gives a way to tell. Record what you find.
5. **Paste behavior.** Check that bracketed paste + Enter submits in the Codex
   composer, and whether long pastes turn into a placeholder (timing-ack path).
6. **Folder trust in a new git worktree.** Does Codex show a trust dialog for
   `../<repo>-worktrees/<task>`? If yes, add its phrases to the Codex dialog
   check.
