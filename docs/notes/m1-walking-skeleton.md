# m1-walking-skeleton — notes, locked interfaces, findings

`agentctl new <name> --goal <g>` creates `../<repo>-worktrees/<name>` on
`agentctl/<name>` from the cwd's HEAD and launches the agent in tmux session
`agentctl-<name>`; `agentctl ls [--json] [--closed]` lists tasks by attention
section. Gate: `scripts/doctor.sh` (= `scripts/check.sh`: gofmt -l, go vet,
go test -race, go mod tidy -diff).

## Stubs: final signatures and contracts (rewrite the file whole)

| File | Owner | Signature | Contract |
|---|---|---|---|
| `internal/adapter/claude/settings.go` | m1-hook-state | `func (a *Adapter) EnsureSettings(home string) (string, error)` | Called by `Start` before every launch. Write `<home>/hooks/claude-settings.json` atomically (tmp+rename, 0600), one command hook per mapped event, command = shell-quoted `a.cfg.AgentctlPath` + ` hook`, `timeout: 5`; return the path. The stub only returns `SettingsPath(home)`. Add any other helpers (e.g. snippet JSON) in this file. |
| `internal/adapter/claude/hook.go` | m1-hook-state | `func (a *Adapter) ParseHook(payload []byte, env adapter.HookEnv) (adapter.HookObservation, error)` | Pure. `HookObservation.State == ""` = record only. Keep only `PromptSHA256`. |
| `internal/adapter/claude/observe.go` | m1-pane-identity | `func (a *Adapter) Observe(ctx context.Context, h adapter.Handle) (adapter.Observation, error)` | Snapshot `h.Identity.Socket`, `domain.ClassifyLiveness`. A non-"no server" probe error must give `domain.UnverifiedLiveness`, never disconnected. |
| `internal/adapter/claude/stop.go` | m1-pane-identity | `func (a *Adapter) Stop(ctx context.Context, h adapter.Handle) error` | Uses `tmux/kill.go` builders; only after verified identity + explicit confirmation. |
| `internal/adapter/claude/deliver.go` | m2-prompt-queue | `func (a *Adapter) Deliver(ctx context.Context, h adapter.Handle, m adapter.Message) (adapter.Receipt, error)` | The only user of the paste/send-keys builders. `adapter.ErrNotReady` if its last identity check fails. |
| `internal/controller/reconcile.go` | m1-pane-identity | `func (c *Controller) Reconcile(ctx context.Context) error` | Already called by `cmd_ls.go` before `Overview` (errors print a warning). One `c.snapshot(ctx, socket)` per socket + `livenessFrom(id, snap, err)` per session. Idempotent; never act on `LivenessUnverified`. |
| `internal/controller/drain.go` | m2-prompt-queue | `type DrainResult struct{ MessageID int64; Delivered bool; Reason string }`; `func (c *Controller) Drain(ctx context.Context, sessionID int64) (DrainResult, error)`; `func (c *Controller) DrainAll(ctx context.Context) error` | TUI calls `DrainAll` every `Tunables.Reconcile`; `deliver` calls `Drain`. `DrainResult` may gain fields. |
| `cmd/agentctl/cmd_<name>.go` | see table | `func cmdX(ctx context.Context, sys *system, args []string) int` | Registered in `commands.go` (do not edit). Names: `cmdTUI` (m2-tui-inbox; bare `agentctl` too), `cmdAdopt`, `cmdClose` (m1-pane-identity), `cmdShow`, `cmdDiff` (m1-inspect), `cmdSend`, `cmdDeliver` (m2-prompt-queue), `cmdHook`, `cmdHooksSnippet` (m1-hook-state), `cmdStats`, `cmdTmuxBinding` (m2-tui-inbox). Stubs print `agentctl <name>: not implemented yet (<issue>)` to stderr, exit 2, and self-register with `var _ = registerStub("<name>", "<issue>")`; rewriting the file whole drops the registration, and `TestStubSubcommandsExit2` checks only the stubs still registered. |

Skeleton tests never assert stub behavior of another card's file (no `ErrNotImplemented` checks), so rewriting a stub cannot
break a skeleton-owned test. Skeleton tests that call `Start` pass `AgentctlPath` and a temp home, so a real `EnsureSettings` works.

### cmd/agentctl helpers (main.go / commands.go)

`sys` fields: `stdin, stdout, stderr, environ []string, cwd, now func() time.Time, executable` (abs agentctl path).
Methods: `sys.getenv(k)`, `sys.openController(ctx)`, `sys.fail(err) int` (prints `agentctl: <one line>`, exit 1),
`sys.usageError(cmd, fmt, ...) int` (exit 2). Helpers: `newFlagSet(sys, name)`, `parseInterspersed(fs, args)`
(flags before/after positionals; `--` ends flags), `flagExit(err)`, `notImplemented(sys, name, issue)`.
Exit codes `exitOK=0`, `exitError=1`, `exitUsage=2`. Read env only via `sys` (tests run `run()` in-process).
`hook` should not use `openController`; open `store.Open(ctx, paths.DB, store.Options{BusyTimeout: store.HookBusyTimeout})`
via hookingest. `cmd` imports only controller/domain (+ hookingest/tui later), not tmux/store.

## Shared APIs other cards build on (all in skeleton-owned files)

**domain** — `AgentState` (+`IsTerminal`, `Valid`, `AgentStates()`), `Confidence`, `TaskState`, `Origin`,
`MessageState` (+`MessageStates()`), `AckMatch`; `CanTransition`, `CanTransitionMessage`, `CanTransitionTask`;
`Section` (`Order`, `Title`, `Sections()`), `MessageSummary` (+`Add`), `ClassifyAttention(AttentionInput) Attention{Section, Label}`;
`Identity` (+`IsZero`), `PaneObservation`, `Liveness{Status, Reason, ExitStatus, Signal, Detail}` (`IsAlive`, `IsVerified`,
`ImpliedState() (state, detail, ok)`), `ClassifyLiveness`, `UnverifiedLiveness`; reasons `pane_missing`, `server_gone`,
`identity_mismatch`, `no_identity`; `SanitizeTerminalText`; `ValidateSlug`, `ErrInvalidSlug`, `MaxSlugLen`;
`Source*` consts (`hook`, `pane`, `timeout`, `launch`, `user`); `Event*` consts + `EventTypes()`.
m2-prompt-queue adds `domain/delivery.go`, `domain/ack.go` (names not used by the skeleton).

**store** — `Open(ctx, path, Options{BusyTimeout, Now})`, `DefaultBusyTimeout` 5s, `HookBusyTimeout` 1.5s;
`ErrNotFound`, `ErrTaskExists`, `ErrTaskHasSession`, `ErrTransitionNotAllowed`; `st.DB()` (tests/fixtures only), `st.Now()`.
Accessors `st.Repos()`, `st.Tasks()`, `st.Sessions()`, `st.Events()` — add methods in your own
`<entity>_<concern>.go`; m2-prompt-queue defines `type Messages` and `func (s *Store) Messages() *Messages` in `messages.go`.
- `Repos`: `Upsert(ctx, root, name)`, `ByPath`, `Get`.
- `Tasks`: `Reserve(ctx, Task)` (ErrTaskExists), `Unreserve(ctx, id)` (only without sessions), `Get`, `Exists`.
- `Sessions`: `Create(ctx, NewSession{..., State: starting|unknown|failed, Events: []EventInput})` (events in the same tx,
  task/session ids auto-filled), `Get`, `LatestForTask`, `ListActive` (non-terminal sessions of running tasks),
  `Transition(ctx, id, to, conf, detail, source) (TransitionResult{From, To, Changed, EventID}, error)`.
- `Events`: `Append(ctx, EventInput{Type, TaskID, SessionID, CauseID, Payload any})`, `Since(ctx, afterID, limit)`, `ForTask`, `LastID`.
- `Overview(ctx, OverviewFilter{RepoID, IncludeClosed}) []OverviewRow{Task, Repo, Session *Session, Messages}`.
- Package-internal for your store files: `s.withTx(ctx, func(*sql.Tx) error)` (BEGIN IMMEDIATE),
  `appendEvent(ctx, q, now, EventInput)`, `getSession(ctx, q, id)`, `(*Sessions).transitionTx(ctx, tx, transitionInput{SessionID, To,
  Confidence, Detail, Source, CauseID, Extra})`, `scanSession/scanTask/scanEvent`, `sessionCols/taskCols/eventCols`, `prefixed()`,
  `ms/nullMS/fromMS/fromNullMS/nullInt`, `isConstraint`, `cleanDetail`.
- Transition semantics: self move = allowed no-op (refreshes confidence/detail, keeps `state_since`, no event); terminal
  self move changes nothing; entering a terminal state sets `ended_at`; detail sanitized, ≤ `MaxDetailLen` (512).
- Timestamps are INTEGER unix **milliseconds**. `sessions.agent_session_id` is written only by sessions_hook.go.

**tmux** — `Runner{Run(ctx, Request{Socket, Commands [][]string, Stdin}) (Result, error)}`, `ExecRunner{Bin, Env, ConfigFile}`,
`Socket{Path|Name}` (zero = `ErrNoSocket`), `Argv`, `EscapeArg`, `EscapeFormat`, `*Error{Commands, Stderr}` (unwraps to
`ErrNoServer`), `Client{Runner, Socket}` with unexported `c.run(ctx, stdin, cmds...)` for new files in this package;
`NewSessionCommands`/`NewSession` → `Created{ServerPID, SessionID, PaneID, PanePID}`; `HasSession`; `SnapshotCommands`/`Snapshot()`
→ `Snapshot{ServerPID, Panes []Pane}` (+`Find`, `Observe(paneID) domain.PaneObservation`); `ListPanes`; `ServerPID`, `SocketPath`;
`ResolveSocket(getenv, uid)`, `SocketFromTMUX`, `CanonicalSocket`, `SameSocket`, `EnvSocket`.
`Pane{ServerPID, SessionID, WindowIndex, PaneID, PanePID, Dead, DeadStatus, DeadSignal, SessionName, CurrentCommand, CurrentPath}`.

**gitx** — `Git{Bin, Env}` with unexported `g.run(ctx, dir, args...)` (adds `-C dir`, `GIT_OPTIONAL_LOCKS=0`, `LC_ALL=C`,
strips `GIT_DIR`/`GIT_WORK_TREE`/...); `MainRepo` → `RepoInfo{Root, Name, CommonDir}`, `Head` → `Head{Ref, SHA}`, `BranchExists`,
`Toplevel` and `MergeBase` (added for m1-pane-identity's adopt), `AddWorktree`, `ListWorktrees` → `[]Worktree`; errors
`ErrNotARepo`, `ErrNoCommits`, `ErrBranchExists`, `ErrWorktreePathExists`, `ErrUnsupportedRepo`; `*Error{Args, Dir, Stderr, ExitCode}`.

**adapter / claude** — `adapter.AgentAdapter` as in ARCHITECTURE.md; `LaunchSpec{TaskID, Goal, Worktree, Home, Socket, AgentCmd}`,
`Handle{Identity, SessionName, Argv}`, `Observation{Liveness, Pane}`, `Message{ID, Body, BodySHA256}`, `Receipt{SentAt, Buffer}`,
`HookEnv{TaskID, Home, TMUX, TMUXPane}`, `HookObservation{Event, AgentSessionID, Cwd, State, Detail, PromptSHA256, Attrs}`,
`ErrNotReady`, `ErrNotImplemented`. `claude.New(Config{Runner, AgentctlPath, Now})`, `a.client(socket)`, `Name`, `SessionName`,
`SessionPrefix`, `SettingsPath(home)`, `LaunchArgv(prefix, settings, goal)`.

**controller** — `Open(ctx, Config{Environ, Paths, Tunables, Now, TmuxRunner, AgentctlPath})`, `Close`, `Paths`, `Tunables`, `Now`,
`Getenv`, `Preflight(ctx)`; `NewTask(ctx, NewTaskRequest{Name, Goal, Cwd}) (NewTaskResult, error)`;
`Overview(ctx, OverviewQuery{Cwd, AllRepos, IncludeClosed}) (Overview{AllRepos, Repo, RepoRoot, Rows []TaskView, Counts, Now}, error)`
with `Overview.Header()` = `agents / <repo> · N running · M need you`; `EventsSince(ctx, after, limit)`, `LastEventID`;
unexported `liveSession(ctx, store.Session) (domain.Liveness, tmux.Pane)`, `snapshot(ctx, socket)`, `livenessFrom`,
`tmuxClient(socket)`, `c.store`, `c.git`, `c.agent`, `c.uid`, `lookPath`. Errors: `*Error{Kind, Msg, Err}` with kinds
`ErrInvalidName, ErrInvalidGoal, ErrAgentCmdInvalid, ErrGitMissing, ErrTmuxMissing, ErrAgentMissing, ErrNotARepo, ErrNoCommits,
ErrTaskExists, ErrTaskNotFound, ErrBranchExists, ErrWorktreeExists, ErrSessionExists, ErrWorktreeFailed, ErrLaunchFailed`;
`userErr(kind, cause, fmt, ...)`, `OneLine`. `Tunables` + `DefaultTunables()` (zero fields default). `EnvAgentCmd`, `DefaultAgentCmd`.
TUI tests: real controller on a temp home (`Config{Environ: env, Now: fixedClock, TmuxRunner: recorder}`), seed with
`store.Open` on `paths.ForHome(home).DB` from `_test.go` files (archtest allows store imports in tests), raw-SQL messages via `st.DB()`.

**paths** — `Resolve(getenv)`, `ForHome(home)`, `EnsureHome` (0700), `Paths{Home, DB, Logs, Hooks, ClaudeSettings, ConfigFile}`, `EnvHome`.

**testenv** — `NewEnv(t) Env` (hermetic; no TMUX/TMUX_PANE), `Env.With/Without/Get/Lookup/Getenv/Strings`, `TempHome`, `RequireTool`,
`BinDir(t, tools...)` (PATH with only those tools), `TempRepo(t, env) *Repo{Root, Env}` (+`Git`, `WriteFile`, `HeadSHA`, `WorktreesDir`),
`GitIn`, `NewTmuxServer(t, env) *TmuxServer{Name, Socket, Env}` (+`Runner`, `Client`, `Run`, `TryRun`, `PID`, `Snapshot`, `Kill`,
`Restart` — restart reuses `%N` with a new server pid), `WaitFor(t, timeout, what, cond)`, `Eventually`.
`testenv.Env` passes `AGENTCTL_TMUX_SOCKET` to the controller; `kill-server` exists only here.

## `agentctl ls --json` (schema_version 1, stable)

```json
{
  "schema_version": 1,
  "generated_at": "RFC3339Nano UTC",
  "scope": {"all_repos": false, "repo": "repo", "repo_root": "/abs/main/root"},
  "header": "agents / repo · 1 running · 0 need you",
  "counts": {"needs_you": 0, "idle": 0, "running": 1, "unknown": 0, "closed": 0},
  "tasks": [{
    "id": "demo", "name": "demo", "goal": "…", "repo": "repo", "repo_root": "/abs",
    "task_state": "running|closed",
    "section": "needs_you|idle|running|unknown|closed", "label": "starting|working|ready|exited|awaiting input|failed|disconnected|unknown|no session|closed|message unconfirmed · attach to check|message failed|queued · attach to deliver|queued · agent exited",
    "agent_state": "starting|ready|working|awaiting_input|unknown|stopped|failed|disconnected|\"\" (no session)",
    "confidence": "hook|pane|none|\"\"",
    "state_since": "RFC3339Nano UTC (task created_at when no session)", "age_seconds": 12,
    "branch": "agentctl/demo", "worktree": "/abs", "base_ref": "main|HEAD", "base_sha": "40-hex",
    "last_detail": "sanitized text", "created_at": "RFC3339Nano UTC",
    "session": null | {"id": 1, "agent_type": "claude", "origin": "launched|adopted", "tmux_socket": "/abs canonical",
                       "tmux_server_pid": 1, "tmux_session_id": "$0", "tmux_session_name": "agentctl-demo",
                       "tmux_pane_id": "%0", "pane_pid": 1, "agent_session_id": "", "ended_at": null | "RFC3339Nano"},
    "messages": {"queued": 0, "delivering": 0, "sent": 0, "acknowledged": 0, "unconfirmed": 0, "failed": 0, "cancelled": 0}
  }]
}
```
`tasks` is always an array; order = section order then `state_since` oldest first. Keys are never omitted.
Text columns: `NAME SECTION STATE CONFIDENCE AGE BRANCH WORKTREE` under the header line (STATE is the raw agent_state;
`-` when no session). Scope: the cwd's main repo; all repos outside a git repo.

## Event payloads written by the skeleton

`task.created {repo, repo_root, branch, worktree, base_ref, base_sha, origin}`;
`session.started {socket, server_pid, session_id, session_name, pane_id, pane_pid, agent_type, argv0}`;
`session.launch_failed {error}`; `agent.state_changed {from, to, source, confidence, detail?}` (+ caller `Extra`, `cause_id` column).

## tmux 3.5a findings (verified on isolated sockets)

1. **Any argv element ending in `;` ends the tmux command** (`fix it;` truncates; the next args become a new command). `EscapeArg`
   rewrites a trailing `;` to `\;`, which tmux turns back into the literal. Applied to every arg of every invocation.
2. **`-c` is format-expanded** (`#{pane_id}`/`#()` in a path); `EscapeFormat` doubles `#`. A nonexistent `-c` dir silently falls back to
   the server's `$HOME` (testenv servers get a temp HOME for this reason). `-e` values and argv are not expanded.
3. `new-session` on an uncreatable socket prints `error creating …` but **exits 0** with empty `-P` output → treated as failure.
4. `-F` output escapes control bytes as `\NNN` but passes tabs/backslashes raw; session names are vis-encoded at creation. Hence tab
   separators with `pane_current_path` last (SplitN). `-P -F` includes `#{pid}` so the full identity comes from the creating call.
5. `set-option -w` with no `-t` after `new-session` in one invocation targets the new window; a dead pane (remain-on-exit) keeps its
   original `pane_pid`, so identity still matches and `pane_dead_status` is readable.
6. `list-panes -a` on a server with zero sessions fails with `no current target` (after `display-message` printed the pid) → empty snapshot.
7. `kill-server` leaves the socket file behind; testenv deletes its own `agentctl-test-*` socket. `-S` paths must stay < ~104 bytes (macOS).

## Deviations from ARCHITECTURE.md / the card

1. **NewTask order**: all fallible checks (slug, goal, tools, repo, task/branch/worktree/tmux-session existence) run *before* the task row
   is reserved (card listed "reserve task row" second). The reservation is still the concurrency guard; this is what makes "no partial
   row" hold. The session row is inserted only after tmux returns the identity (no identity-less `starting` window a reconcile could
   misjudge); a failed launch inserts the session directly in `failed` with event `session.launch_failed` (new constant).
2. Goals starting with `-` are rejected (claude would parse them as flags, e.g. `--dangerously-skip-permissions`); goals ≤ 64 KiB, no
   NUL; an empty goal is omitted from argv. Director: check in the m3 demo whether `claude --settings f -- "<goal>"` works; if so, `--`
   could replace the rejection.
3. Attention adds `queued · agent exited` (queued message on a `stopped` agent → NEEDS YOU) beside the spec's queued-on-unknown rule.
4. Liveness adds status `unverified` (probe error ≠ disconnected; never sticky) and reason `no_identity`.
5. archtest: domain/tui import rules apply to non-test files (TUI tests may seed via store); exec, kill and gitx-vocabulary rules apply to
   tests too; paste/send-keys literals are allowed in tests per the spec. The `send` alias of send-keys is not detectable by literal scan.
   gitx forbidden tokens also include `-d`, `--delete`, `prune`, `rm`, `remove`, `-f`.
6. Extra domain constant `message.failed`; extra gitx `Toplevel`/`MergeBase`; extra tmux `HasSession` (new refuses an existing
   `agentctl-<name>` session with `ErrSessionExists`).
7. `cmd` has no tmux runner injection (keeps `cmd → controller` only); TUI tests inject via `controller.Config.TmuxRunner`.

## Hermeticity guards (added after an incident, see below)

- `controller.NewTask` requires an absolute `Cwd`, and `gitx` refuses an empty or relative `-C` dir. `git -C ""` resolves against the
  process cwd, which inside the test binary is this checkout, whose main repo is the user's.
- `testenv.NewEnv` sets `AGENTCTL_TMUX_SOCKET=/tmp/agentctl-test-no-server-<rand>/none` (a directory that is never created). A test
  that forgets `NewTmuxServer` can therefore never resolve to the user's default server; `NewTmuxServer` overrides it.

**Incident (needs the user's cleanup; my cleanup command was denied by the permission classifier).** Before those guards existed, a
red-phase test (`TestNewTaskRequiresAbsoluteCwd`, with `Cwd: ""` and no isolated server) ran one real `NewTask`. It created:
tmux session `agentctl-demo` (`$1`, pane `%17`, `sleep 600`; dead pane after ~00:07) on the user's **default** server, worktree
`/Users/jpark/Code/agent-tui-worktrees/demo` and branch `agentctl/demo` (at `adc1be7`, identical to `tb/m1-walking-skeleton`, so no
work is lost) in `/Users/jpark/Code/agent-tui`. The user's `main` session was not touched. To clean up:

```sh
tmux kill-session -t '=agentctl-demo'
git -C /Users/jpark/Code/agent-tui worktree remove /Users/jpark/Code/agent-tui-worktrees/demo
git -C /Users/jpark/Code/agent-tui branch -D agentctl/demo
rmdir /Users/jpark/Code/agent-tui-worktrees
```

## Notes for the director

- No new modules after this card; `go list -m all` shows every pin at the ARCHITECTURE.md version, teatest at
  `v2.0.0-20261101151554-27ecf3e4d454` (imports `charm.land/bubbletea/v2`). No migrations beyond `0001_init.sql`.
- `scripts/check.sh` runs gofmt over every `.go` file outside dot-directories (so `.tinkerbell/worktrees/*` in the main tree is skipped).
- Whole suite ≈ 6 s; it was run inside the user's tmux (`$TMUX` set) and passes.
