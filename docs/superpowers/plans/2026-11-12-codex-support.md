# Codex Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Codex CLI agents behave like Claude agents in agentctl (attention sections, hook-driven state, gated prompt delivery), launched by `agentctl new --agent codex` or by the `agentctl codex` wrapper, plus a new INITIALSTATE attention section for every agent that has not sent its first hook.

**Architecture:** A second adapter, `internal/adapter/codex`, parses Codex hook payloads and builds a launch argv that passes agentctl's hooks with `-c` (which also forces Codex's embedded mode, the only mode where hooks see the right pane). The controller and hookingest pick an adapter per session by `sessions.agent_type` instead of hard-coding Claude. The wrapper registers the current pane (new origin `wrapped`), runs Codex as a child, and records its exit.

**Tech Stack:** Go 1.27, SQLite (modernc), tmux ≥ 3.3, bubbletea v2 TUI, `cmd/fakeagent` for hermetic tests.

**Spec:** `docs/superpowers/specs/2026-11-12-codex-support-design.md` (read it first; this plan argues from it). Also `docs/ARCHITECTURE.md` (invariants 1–10, hook→state table, delivery rules).

## Global Constraints

- All ten invariants in `docs/ARCHITECTURE.md` §Invariants hold. Gate: `scripts/check.sh` (gofmt, vet, `go test -race ./...`, mod tidy) is green after every task.
- Claude behavior is unchanged except the attention change in Task 1. Claude stays the default agent; `""` agent means `claude`.
- Hook stdout is always empty; `agentctl hook` always exits 0 (invariant 7). This includes `hook --agent codex` and any bad `--agent` value.
- agentctl never passes `--dangerously-bypass-hook-trust`, never sends keys to a trust screen, never edits `~/.codex/*`.
- Tests never touch `~/.codex`, the user's tmux server, or a real Codex daemon (invariant 8).
- Codex hook command, byte-exact: `'<abs agentctl path>' hook --agent codex` (single-quoted path, like `claude.HookCommand`).
- Codex hook flag value, per event: `hooks.<Event>=[{hooks=[{type="command",command="<TOML basic-string-escaped hook command>",timeout=5}]}]`, each as its own argv element after a separate `-c` element.
- Subscribed Codex events, in this order: `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PermissionRequest`, `PreCompact`, `Stop`, `Interrupt`, `SessionEnd`.
- Section title copy: `INITIALSTATE`. Details: `waiting for first prompt · attach to start`, `hook trust: attach to answer`.
- Display order: NEEDS YOU, IDLE, RUNNING, INITIALSTATE, UNKNOWN, (closed).
- Commit messages and PR text: ASD-STE100 Simplified Technical English (user's global rule). Work on branch `codex-support`, never on `main`.

## Review Focus

1. **Wrapper killed with SIGKILL** (it can't record Codex's exit): a queued message must not be pasted into the user's shell → Task 8 test `TestDrainWrappedRefusesWhenCodexNotForeground`.
2. **`agentctl codex` outside tmux** (`$TMUX` or `$TMUX_PANE` unset): clear error, exit non-zero, nothing registered → Task 7 test `TestCmdCodexOutsideTmux`.
3. **agentctl's Codex hook firing inside `codex app-server`** (user copied it to `~/.codex/hooks.json`): orphan `codex_daemon`, no state change, even though `$TMUX_PANE` looks valid → Task 5 test `TestCodexHookFromDaemonIsOrphan`.
4. **`agentctl codex` twice in one pane**: refused while the first session is live; after it exited, a new session on the same task → Task 7 tests `TestRegisterWrappedRefusesLive`, `TestRegisterWrappedReusesExitedTask`.
5. **Upgrading a real registry** (existing tasks, sessions, messages with FK to sessions) through migration 0002: all rows and FKs survive, old sessions read `HasHook` correctly → Task 2 test `TestMigration0002KeepsRows`.

---

### Task 1: Domain — origin `wrapped`, INITIALSTATE section, Codex dialog phrases

**Files:**
- Modify: `internal/domain/state.go` (Origin), `internal/domain/attention.go`, `internal/domain/foldertrust.go`
- Test: `internal/domain/attention_origin_test.go`, `internal/domain/foldertrust_test.go`

**Interfaces:**
- Produces: `domain.OriginWrapped Origin = "wrapped"` (`Valid()` true); `domain.SectionInitial Section = "initial"` with `Title() == "INITIALSTATE"`, `Order()` between running and unknown, included in `Sections()`; `AttentionInput.HasHook bool`; `domain.HookTrustDetail = "hook trust: attach to answer"`; `domain.DetailAwaitingFirstPrompt = "waiting for first prompt · attach to start"`; `func ShowsCodexHookTrustDialog(screen string) bool`; `func SlugFrom(s string) string` (lower-case, non-`[a-z0-9]` runs → `-`, trimmed, ≤ 40 bytes, `""` if nothing left; result passes `ValidateSlug` or is `""`).

- [ ] **Step 1: Write failing table tests** in `attention_origin_test.go`, `TestClassifyAttentionInitialState`:

| AgentState | Origin | HasHook | Queued | → Section | Label |
|---|---|---|---|---|---|
| starting | launched | false | 0 | initial | `no events yet` |
| starting | wrapped | false | 0 | initial | `no events yet` |
| starting | launched | true | 0 | running | `starting` |
| starting | launched | false | 1 | needs_you | `queued · attach to deliver` |
| unknown | launched | false | 0 | needs_you | `silent since launch · attach to check` |
| unknown | wrapped | false | 0 | needs_you | `silent since launch · attach to check` |
| unknown | adopted | false | 0 | initial | `no events yet` |
| unknown | adopted | true | 0 | unknown | `unknown` |
| working | wrapped | true | 0 | running | `working` |

Plus `TestSectionsOrder`: `Sections()` == `[needs_you idle running initial unknown closed]`, `SectionInitial.Title() == "INITIALSTATE"`. `TestShowsCodexHookTrustDialog`: true for screens containing `Trust all and continue` or `Continue without trusting (hooks won't run)` (any case, wrapped across lines, inside box-drawing borders); false for `trust` alone and for Claude's folder-trust text. `TestSlugFrom`: `"arize"`→`"arize"`, `"My Repo!"`→`"my-repo"`, `"---"`→`""`, 60×`a` → 40×`a`.

- [ ] **Step 2: Run** `go test ./internal/domain/ -run 'InitialState|SectionsOrder|CodexHookTrust|SlugFrom'` — Expected: FAIL (undefined names).
- [ ] **Step 3: Implement.** In `ClassifyAttention`, after the agent-state NEEDS YOU cases: extend the queued rule to `m.Queued > 0 && !in.HasHook && (starting||unknown)` as well as the existing `unknown` case; then `starting && !HasHook → initial`; `unknown && origin ∈ {launched, wrapped} → needs_you silent`; `unknown && !HasHook → initial`; else unknown. Update the doc comment's rule list. Codex phrases go in a separate `codexHookTrustPhrases` list reusing `normalizeScreen`.
- [ ] **Step 4: Run** `go test ./internal/domain/...` — Expected: PASS (fix any existing golden expectations only where Task-1 rules intentionally changed them; list each changed case in the commit message).
- [ ] **Step 5: Commit** `git commit -m "feat(domain): add the wrapped origin and the INITIALSTATE section"`

### Task 2: Store — migration 0002, `first_hook_at`, wiring `HasHook`

**Files:**
- Create: `internal/store/migrations/0002_codex.sql`
- Modify: `internal/store/sessions.go` (Session fields, `sessionCols`, scan), `internal/store/sessions_hook.go` (RecordHook), `internal/store/sessions.go` / `sessions_reconcile.go` wherever origin is validated, `internal/controller/overview.go:140` (pass `HasHook`)
- Test: `internal/store/store_test.go` (migration), `internal/store/sessions_hook_test.go`

**Interfaces:**
- Consumes: `domain.OriginWrapped`, `AttentionInput.HasHook` (Task 1).
- Produces: `store.Session.FirstHookAt time.Time` (zero when NULL) and `func (s Session) HasHook() bool`; `RegisterAdopted` and `Sessions().Create` accept `OriginWrapped`.

- [ ] **Step 1: Write failing tests.** `TestMigration0002KeepsRows`: open a DB at migration 1 only (use `loadMigrations` on a filtered FS), insert a repo, task, launched session, adopted session, and a message referencing a session; then run all migrations; assert every row is unchanged, `PRAGMA foreign_key_check` returns no rows, an insert with origin `wrapped` succeeds, origin `bogus` still fails the CHECK, and both old sessions have `HasHook() == false`. `TestRecordHookSetsFirstHookAt`: first `RecordHook` sets `FirstHookAt` to the store clock; a second one leaves it unchanged; a record-only hook (State `""`) also sets it.
- [ ] **Step 2: Run** `go test ./internal/store/ -run 'Migration0002|FirstHookAt'` — Expected: FAIL.
- [ ] **Step 3: Implement** `0002_codex.sql`: `PRAGMA defer_foreign_keys = ON;` then the SQLite table-rebuild procedure — `CREATE TABLE sessions_new (...)` identical to 0001 except `origin ... CHECK (origin IN ('launched','adopted','wrapped'))` and a new `first_hook_at INTEGER` column; copy rows; `DROP TABLE sessions`; `ALTER TABLE sessions_new RENAME TO sessions`; recreate `sessions_task`, `sessions_pane`, `sessions_state` indexes. Backfill: `first_hook_at` = earliest `hook.received` event time for that session if any (so old sessions with hooks keep reading as HasHook). Add `first_hook_at` to `sessionCols`/scan. In `RecordHook`, in the same transaction: `UPDATE sessions SET first_hook_at = ? WHERE id = ? AND first_hook_at IS NULL`. Pass `HasHook: s.HasHook()` in `overview.go`.
- [ ] **Step 4: Run** `go test -race ./internal/store/... ./internal/controller/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(store): record the first hook and allow the wrapped origin"`

### Task 3: Codex adapter

**Files:**
- Create: `internal/adapter/hooktext.go` (shared helpers), `internal/adapter/codex/codex.go`, `internal/adapter/codex/hook.go`, `internal/adapter/codex/start.go`
- Modify: `internal/adapter/claude/hook.go` (use the shared helpers instead of private `hookLine`/`bound`)
- Test: `internal/adapter/codex/hook_test.go`, `internal/adapter/codex/start_test.go`, `internal/adapter/hooktext_test.go`

**Interfaces:**
- Produces:
  - `adapter.HookLine(s string) string`, `adapter.Bound(s string, n int) string` (moved from claude, same behavior).
  - `codex.Name = "codex"`; `type codex.Config struct { Runner tmux.Runner; AgentctlPath string; Now func() time.Time }`; `func codex.New(cfg Config) *Adapter` implementing `adapter.AgentAdapter`. `Observe`, `Deliver`, `Stop` delegate to an embedded `*claude.Adapter` built from the same config (this keeps the archtest rules `paste-sendkeys` and `kill-commands` unchanged).
  - `func codex.HookCommand(agentctlPath string) string` → `'<path>' hook --agent codex`.
  - `func codex.HookEvents() []string` (Global Constraints order, a copy).
  - `func codex.HookFlags(agentctlPath string) ([]string, error)` → `["-c", "hooks.SessionStart=[…]", "-c", …]`; error for empty, relative, or control-char paths (reuse claude's validation rules).
  - `func codex.LaunchArgv(prefix []string, agentctlPath, goal string) ([]string, error)` → prefix + HookFlags + goal (omitted when empty).
  - `codex.Start` creates `agentctl-<task>` exactly like `claude.Start` (same env, remain-on-exit, one tmux invocation) with `LaunchArgv`.

- [ ] **Step 1: Write failing tests.** `TestCodexParseHook` table (payloads copied from the spec's probe output, e.g. the `Stop` payload with `turn_id`, `permission_mode`, `last_assistant_message`):

| event / fields | → State | Detail |
|---|---|---|
| SessionStart source=startup | `""` | `session started (startup)` |
| SessionStart source=compact | `""` | `""` |
| UserPromptSubmit prompt="hi" | working | `prompt submitted`; `PromptSHA256` = sha256("hi") |
| PreToolUse tool_name=Bash | working | `tool: Bash` |
| PostToolUse tool_name=Bash | working | `tool done: Bash` |
| PreCompact trigger=auto | working | `compacting (auto)` |
| PermissionRequest tool_name=Bash | awaiting_input | `permission request: Bash` |
| Stop | ready | `turn finished` |
| Interrupt | ready | `interrupted` |
| SessionEnd | `""` | `session ended` |
| PostModelRequest | `""` | `""` |

Also: `last_assistant_message`, `tool_input`, `transcript_path` never appear in the observation; `AgentSessionID` = `session_id`; malformed JSON / missing event name → error. `TestCodexHookFlags`: for `/opt/agentctl` the first two elements are exactly `-c` and `hooks.SessionStart=[{hooks=[{type="command",command="'/opt/agentctl' hook --agent codex",timeout=5}]}]`; for a path containing `"` and `\` the TOML string escapes them; output is identical across two calls. `TestCodexLaunchArgv`: prefix `["/usr/bin/codex"]`, goal `"do x"` → prefix, 18 flag elements, `"do x"` last; empty goal → no trailing element. `TestCodexStart` mirrors `claude/start_test.go` with the fake runner.
- [ ] **Step 2: Run** `go test ./internal/adapter/...` — Expected: FAIL.
- [ ] **Step 3: Implement** per Interfaces. The payload struct decodes only `session_id, cwd, hook_event_name, source, prompt, tool_name, trigger, reason`.
- [ ] **Step 4: Run** `go test -race ./internal/adapter/... ./internal/archtest/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(adapter): add the Codex adapter"`

### Task 4: fakeagent Codex mode

**Files:**
- Create: `cmd/fakeagent/codex.go`
- Modify: `cmd/fakeagent/main.go` (`parseConfig`), `cmd/fakeagent/hooks.go` (payload shape), `cmd/fakeagent/trust.go`
- Test: `cmd/fakeagent/fakeagent_test.go`

**Interfaces:**
- Produces: fakeagent accepts `--codex` as its first argument. In this mode it reads hooks from repeated `-c hooks.<Event>=<TOML>` flags (parse only the shape agentctl emits; reject others with exit 2), ignores other `-c` values, and takes an optional positional prompt. Behavior differences from Claude mode: no hook at startup; `SessionStart` (source `startup`) fires immediately before the first `UserPromptSubmit`; payloads carry `turn_id`, `permission_mode`, `model`; `#ask` fires `PermissionRequest` (no `Notification`); `Stop` carries `last_assistant_message`. Env `FAKEAGENT_HOOK_TRUST=1` prints a screen containing `Review hooks`, `Trust all and continue`, `Continue without trusting (hooks won't run)` and fires no hook until answered (`2`/Enter trusts and continues, `3` continues without hooks). Its process name stays `fakeagent`. Update the package doc comment.

- [ ] **Step 1: Write failing tests** in `fakeagent_test.go`: `TestCodexModeLateSessionStart` (no hook before input; after a submit the hook log order is SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Stop); `TestCodexModeHookTrustGate` (with `FAKEAGENT_HOOK_TRUST=1` no hook fires until `2`; with `3` none ever fires); `TestCodexModeRejectsUnknownHookShape`.
- [ ] **Step 2: Run** `go test ./cmd/fakeagent/ -run CodexMode` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./cmd/fakeagent/` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "test(fakeagent): add a Codex mode"`

### Task 5: Hook ingest per agent, daemon guard, session-id guard

**Files:**
- Create: `internal/hookingest/codexguard.go`
- Modify: `internal/hookingest/hookingest.go` (Config, parse dispatch), `internal/hookingest/goal.go` (Claude only), `internal/hookingest/resolve.go` (use `adapter` pane helpers only through the chosen adapter package; add orphan reasons), `cmd/agentctl/cmd_hook.go`
- Test: `internal/hookingest/codexguard_test.go`, `internal/hookingest/hookingest_test.go`, `cmd/agentctl/cmd_hook_test.go`

**Interfaces:**
- Consumes: `codex.New(...).ParseHook`, `codex.Name` (Task 3); `store.Session.AgentType`, `AgentSessionID`.
- Produces: `hookingest.Config.Agent string` (`""`→claude); `hookingest.Config.ParentCommand func() (string, error)` (default: `ps -o command= -p <os.Getppid()>`); orphan reasons `OrphanCodexDaemon = "codex_daemon"`, `OrphanAgentMismatch = "agent_mismatch"`, `OrphanUnknownAgent = "unknown_agent"`; `func guardForeignSession(sess store.Session, obs adapter.HookObservation) adapter.HookObservation`.

Rules:
- `cmd_hook.go` reads `--agent <v>` / `--agent=<v>` from args by hand (no flag package output); anything else is ignored. Never writes to stdout.
- Unknown agent → log error, record nothing, exit 0.
- Agent codex and `ParentCommand` is a `codex app-server …` process (basename `codex`, first arg `app-server`) → `recordOrphan(OrphanCodexDaemon)` if the registry exists, never resolve. A `ParentCommand` error does not block (fail open: continue to resolve; log it).
- Resolved session's `AgentType` ≠ hook agent → orphan `agent_mismatch`.
- Codex only: `guardForeignSession` — when the session has an `AgentSessionID`, the hook's differs, and the event is not `SessionStart` with source `clear`/`resume`/`fork`, return the observation with `State=""`, `Detail=""`, `AgentSessionID=""` and `Attrs["foreign_session"]="1"` (recorded, no state change, stored id untouched).
- `withGoalHold` runs only for `claude` sessions.

- [ ] **Step 1: Write failing tests.** `TestCodexHookFromDaemonIsOrphan` (ParentCommand returns `/x/codex app-server --listen unix://`; env has a matching task id and pane; outcome Orphan `codex_daemon`; session state unchanged). `TestCodexHookEmbeddedApplies` (ParentCommand `codex`; `Stop` on a working codex session → ready, Applied). `TestCodexForeignSessionIgnored` (session agent id `A`; `Stop` from `B` → Applied false, agent id still `A`, `hook.received` has `foreign_session`). `TestCodexSessionStartClearRebinds` (SessionStart source clear from `B` → agent id `B`). `TestHookAgentMismatch` (claude hook into codex session → orphan). `TestCmdHookAgentFlagNoStdout` (args `--agent codex`, `--agent=bogus`, `--bogus`: stdout empty, exit 0).
- [ ] **Step 2: Run** `go test ./internal/hookingest/ ./cmd/agentctl/ -run 'Codex|AgentMismatch|AgentFlag'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/hookingest/... ./cmd/agentctl/...` — Expected: PASS (all existing Claude tests unchanged).
- [ ] **Step 5: Commit** `git commit -m "feat(hook): parse Codex hooks and refuse hooks from the Codex daemon"`

### Task 6: Controller adapter registry, `new --agent codex`, per-agent reconcile

**Files:**
- Modify: `internal/controller/controller.go` (registry), `newtask.go`, `adopt.go`, `drain.go`, `close.go`, `preflight.go`, `errors.go`, `reconcile.go`, `foldertrust.go`, `cmd/agentctl/cmd_new.go`, `cmd/agentctl/commands.go` (summary text only)
- Test: `internal/controller/controller_test.go`, `reconcile_test.go`, `foldertrust_test.go`, `cmd/agentctl/` new-command test

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: `func (c *Controller) agentFor(agentType string) (adapter.AgentAdapter, error)` (`""`→claude; unknown → `ErrInvalidAgent`); `NewTaskRequest.Agent string`; `EnvCodexCmd = "AGENTCTL_CODEX_CMD"`, `DefaultCodexCmd = []string{"codex"}`; `func (c *Controller) agentCommand(agent string) ([]string, error)`; `ErrInvalidAgent`, `ErrCodexMissing` (hint: install Codex or set `AGENTCTL_CODEX_CMD`).

Rules:
- Every `c.agent` use selects by the session's `AgentType` (drain, close/stop) or the request's agent (new, adopt keeps claude).
- `new --agent codex` without `--goal`: session created `starting`/`none` with detail `domain.DetailAwaitingFirstPrompt`.
- Reconcile startup-dialog check, still only while `starting`/`none`: claude → `ShowsFolderTrustDialog` → `FolderTrustDetail`; codex → `ShowsCodexHookTrustDialog` → `HookTrustDetail`. Both → `unknown`/`none`, source pane_text.
- Reconcile `StartingTimeout` is **skipped** for codex sessions that are expected to be silent: origin `wrapped`, or task goal empty. (Plan simplification, reported to the user: the spec's "wrapper given a positional prompt" timeout case is not built, because the wrapper can't reliably tell a prompt from other user args. A trust screen still reaches NEEDS YOU.)
- `startingTimeoutDetail` for codex says `no hook within <d> — Codex may be showing its hook-trust screen; attach to check`.
- `cmd_new.go`: `--agent` flag (`claude`|`codex`, default `claude`); summary `new <name> [--agent claude|codex] --goal <goal>`.

- [ ] **Step 1: Write failing tests.** `TestNewTaskCodexUsesCodexArgv` (fake tmux runner records argv: starts with resolved `AGENTCTL_CODEX_CMD[0]`, contains `codex.HookFlags` output, session `agent_type` = codex). `TestNewTaskCodexNoGoalDetail`. `TestNewTaskBadAgent` (`ErrInvalidAgent`, nothing written). `TestReconcileCodexHookTrust` (captured screen with Codex trust text → unknown, `HookTrustDetail`). `TestReconcileCodexNoGoalNoTimeout` (codex no-goal session past `StartingTimeout` stays `starting`). `TestReconcileClaudeTimeoutUnchanged`. `TestDrainUsesSessionAdapter` (codex session drains through the codex adapter).
- [ ] **Step 2: Run** `go test ./internal/controller/ -run 'Codex|BadAgent|SessionAdapter|TimeoutUnchanged'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/controller/... ./cmd/agentctl/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(controller): launch Codex agents with agentctl new --agent codex"`

### Task 7: `agentctl codex` wrapper

**Files:**
- Create: `internal/controller/wrap.go`, `cmd/agentctl/cmd_codex.go`
- Modify: `cmd/agentctl/commands.go` (add `{"codex", "codex [--name <name>] [codex args…]: run Codex here and track it", cmdCodex}`), `internal/domain/state.go` or `events.go` (`SourceWrapper = "wrapper"`, `EventSessionWrapped`)
- Test: `internal/controller/wrap_test.go`, `cmd/agentctl/cmd_codex_test.go`

**Interfaces:**
- Consumes: `codex.LaunchArgv`, `agentCommand("codex")`, `domain.SlugFrom`, `OriginWrapped`, store `RegisterAdopted` / `Sessions().Create`.
- Produces:
  - `type WrapRequest struct { Name string; PaneID string; Cwd string; UserArgs []string }`
  - `type WrapResult struct { TaskID string; SessionID int64; Identity domain.Identity; Reused bool; Argv []string; Env []string }` (`Env` = the extra `AGENTCTL_TASK_ID=…`, `AGENTCTL_HOME=…` entries)
  - `func (c *Controller) RegisterWrapped(ctx context.Context, req WrapRequest) (WrapResult, error)`
  - `func (c *Controller) RecordWrappedExit(ctx context.Context, sessionID int64, exitCode int) error`

Rules:
- `RegisterWrapped`: pane must be live on the resolved socket (reuse `adoptablePane`/`paneLocation`; repo/base rules as adopt). New pane → new task, origin `wrapped`, agent `codex`, state `starting`/`none`, detail `DetailAwaitingFirstPrompt`, events `task.created` + `session.wrapped` in one transaction. Pane already bound: latest session terminal (`stopped`/`failed`/`disconnected`) → new session on that task (`Reused=true`, `--name` ignored); otherwise `ErrPaneRegistered` ("this pane already runs agentctl task <id>"). Name: `--name`, else `SlugFrom(basename(worktree)) + "-codex"`, then `-2`, `-3`… on collision; empty slug → `codex`.
- `RecordWrappedExit`: `0` → `stopped`, else `failed`; detail `codex exited (code N)`; source `wrapper`; through the normal guarded transition (a session already terminal is left alone).
- `cmdCodex`: parse leading `--name <n>` / `--name=<n>`, an optional `--`, rest = UserArgs. Require `$TMUX` and `$TMUX_PANE` (else exit 1: `agentctl codex must run inside tmux`). Open the controller, `RegisterWrapped`, **close the controller** before starting the child. Start the child with `os/exec` (stdin/stdout/stderr = the process's, env = process env + `WrapResult.Env`). While waiting: ignore `SIGINT`, `SIGQUIT` (the child gets them from the terminal); forward `SIGTERM`, `SIGHUP` to the child. On exit: reopen the controller, `RecordWrappedExit` with the child's exit code (signal death → 128+sig; start failure → 127 and the error on stderr), exit with that code. Print one line to stderr before starting: `agentctl: tracking this pane as <task>`.

- [ ] **Step 1: Write failing tests.** `TestRegisterWrappedNewPane` (task + session rows, origin wrapped, agent codex, argv = codex cmd + hook flags + user args, env has task id). `TestRegisterWrappedDefaultName` (`arize` → `arize-codex`, second → `arize-codex-2`). `TestRegisterWrappedRefusesLive`. `TestRegisterWrappedReusesExitedTask`. `TestRecordWrappedExit` (0 → stopped, 3 → failed `codex exited (code 3)`, already stopped stays). `TestCmdCodexOutsideTmux`. `TestCmdCodexEndToEnd` (testenv tmux server; pane runs `agentctl codex --name w` with `AGENTCTL_CODEX_CMD='["<fakeagent>","--codex"]'`; type a prompt → session goes working then ready; type `#exit 0` → session `stopped`, pane still alive showing the shell).
- [ ] **Step 2: Run** `go test ./internal/controller/ ./cmd/agentctl/ -run 'Wrapped|CmdCodex'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/controller/... ./cmd/agentctl/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(cli): add agentctl codex to run Codex in the current pane"`

### Task 8: Shell-paste guard for wrapped sessions

**Files:**
- Modify: `internal/domain/delivery.go`, `internal/controller/drain.go:141-154`
- Test: `internal/domain/delivery_test.go`, `internal/controller/drain_test.go`

**Interfaces:**
- Produces: `DeliveryCheck.ForegroundMismatch bool`; `domain.LabelNotForeground = "codex not running in pane · attach to check"` (CanDeliver returns it, ranked right after the liveness/terminal checks); `func (c *Controller) codexForegroundNames() []string` = `{"codex", basename(agentCommand("codex")[0])}`.

Rule: for origin `wrapped` only, `ForegroundMismatch = pane.CurrentCommand ∉ codexForegroundNames()`, using the pane from the same `liveSession` probe. On mismatch the message reverts to `queued` (existing revert path) and nothing is pasted.

- [ ] **Step 1: Write failing tests.** `TestCanDeliverForegroundMismatch` (ready/hook/alive + mismatch → false, `LabelNotForeground`). `TestDrainWrappedRefusesWhenCodexNotForeground` (wrapped session ready/hook, pane's current command `zsh` → message stays queued, fake runner saw no paste). `TestDrainWrappedDeliversWhenCodexForeground` (current command `fakeagent` → delivered). Launched/adopted sessions ignore the field.
- [ ] **Step 2: Run** `go test ./internal/domain/ ./internal/controller/ -run 'Foreground'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/domain/... ./internal/controller/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "fix(deliver): do not paste when Codex is not in the wrapped pane"`

### Task 9: Adopt hint and TUI

**Files:**
- Modify: `internal/controller/adopt.go` (`AdoptCandidates.Hints []string`, `AdoptResult.Warning string`), `cmd/agentctl/cmd_adopt.go`, `internal/tui/list.go`, `internal/tui/detail.go`, `internal/tui/header.go`, `internal/tui/newtask_view.go`, `internal/tui/adopt_view.go`
- Test: `internal/controller/adopt_test.go`, `internal/tui/golden_test.go` + `internal/tui/testdata/*` (regenerate only the goldens this task changes), `internal/tui/manage_test.go`

Rules:
- Listing (not `--all`): every unregistered live pane whose `pane_current_command` is `codex` adds hint `pane %N runs codex; agentctl can't track it. Restart it with: agentctl codex`. Adopting such a pane succeeds with that warning. CLI and adopt view print the hints.
- TUI: `SectionInitial` renders with the muted style (same as unknown) in list, detail and footer logic; header `N running` excludes it. When the visible task set has more than one agent type, each row shows a short agent tag (`claude`/`codex`). The `n` form gets an agent field (toggle claude/codex, default claude) passed as `NewTaskRequest.Agent`.

- [ ] **Step 1: Write failing tests.** `TestAdoptCandidatesCodexHint`; golden `initial_section` (one INITIALSTATE row muted, header counts); golden `mixed_agents` (tags shown) and unchanged single-agent goldens; `TestNewTaskFormAgentToggle`.
- [ ] **Step 2: Run** `go test ./internal/controller/ ./internal/tui/ -run 'CodexHint|Initial|Mixed|AgentToggle'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/controller/... ./internal/tui/...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -m "feat(tui): show INITIALSTATE and Codex agents"`

### Task 10: End-to-end tests and docs

**Files:**
- Create: `internal/e2e/codex_test.go`
- Modify: `docs/ARCHITECTURE.md` (adapter section: Codex launch + hook table; attention table with INITIALSTATE; origin `wrapped`; delivery rule for wrapped; invariants unchanged), `README.md` (Codex section: `agentctl codex`, `new --agent codex`, trust once in `/hooks`, normal `codex` can't be tracked, `-c` may override your own Codex hooks), `docs/kanban.md` + a card file per repo convention

- [ ] **Step 1: Write the e2e tests** on the test tmux socket with fakeagent `--codex`: `TestE2ECodexNewGoalDeliver` (new with goal → working → ready; `send` delivers once; fakeagent log shows nothing arrived while working). `TestE2ECodexPermissionNoPaste` (`#ask` → NEEDS YOU; queued send not pasted until answered and `Stop`). `TestE2ECodexHookTrustNeedsYou` (`FAKEAGENT_HOOK_TRUST=1` → NEEDS YOU `hook trust: attach to answer`; no keys sent). `TestE2ECodexWrapperInitialState` (wrapper, no prompt → INITIALSTATE past `StartingTimeout`; `send` → NEEDS YOU `queued · attach to deliver`). `TestE2EAdoptedNoHooksInitialState`.
- [ ] **Step 2: Run** `go test -race ./internal/e2e/ -run 'Codex|InitialState'` — Expected: PASS (fix code, not tests, on failure).
- [ ] **Step 3: Update docs** (`internal/e2e/readme_test.go` checks README claims: keep it green).
- [ ] **Step 4: Run** `scripts/check.sh` — Expected: all steps `ok`, exit 0.
- [ ] **Step 5: Commit** `git commit -m "test(e2e): cover Codex agents and update the docs"`

### Task 11: Real-Codex check (with the user)

Not automatable: it needs the user to trust hooks in Codex's `/hooks` screen. Run with the user, record results in `docs/notes/codex-support.md`, and resolve the spec's open questions:

- [ ] `agentctl codex --name probe` in a scratch git repo: trust screen → NEEDS YOU; user trusts; send `ok` prompt → RUNNING → IDLE.
- [ ] Exit, run `agentctl codex` again in a new pane: **no trust screen** (open question 1). If Codex asks again, stop and bring the fallback (opt-in `~/.codex/hooks.json` writer) to the user before building it.
- [ ] Trigger a permission prompt → NEEDS YOU; approve → RUNNING (question 2).
- [ ] Press Esc mid-turn: record whether `Stop` also fires; adjust the `Interrupt` mapping if needed (question 3).
- [ ] `agentctl send` a long multi-line prompt: submitted and acknowledged (question 5).
- [ ] `agentctl new x --agent codex --goal "…"`: record whether a folder-trust dialog appears in the new worktree (question 6); questions 4 noted only.
- [ ] Commit the notes: `git commit -m "docs: record the real Codex check"`
