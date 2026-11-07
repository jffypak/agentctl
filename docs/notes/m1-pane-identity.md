# m1-pane-identity — notes, APIs, findings

Reconcile, adopt and close, all keyed on the explicit pane identity
(socket, server pid, session id, pane id, pane pid). Gate: `scripts/doctor.sh`.
No new modules, no migrations.

## What exists now (APIs other cards build on)

**controller**
- `Reconcile(ctx) error` (reconcile.go). Already called by `ls`; the TUI tick calls it every `Tunables.Reconcile`.
  One `c.snapshot` per socket, `livenessFrom` per active session. Verdicts: dead 0 → `stopped` "exit 0"; dead N →
  `failed` "exit N"; signal → `failed` "signal N"; missing pane / other server pid / other pane pid / no server →
  `disconnected` "disconnected: <reason>" + `session.disconnected{reason, from}`; all source `pane`, confidence `pane`.
  `starting` older than `StartingTimeout` → `unknown`/`none`, source `timeout`, detail `no hook within 30s`.
  Returns the joined probe errors (non-"no server") so callers can warn; unverified probes never change a pane verdict.
  Idempotent and safe under concurrent reconcilers (one event per change).
- `ListAdoptCandidates(ctx, CandidateQuery{All}) (AdoptCandidates{Socket, Commands, Panes []AdoptCandidate}, error)`;
  `AdoptCandidate{PaneID, PanePID, SessionID, SessionName, WindowIndex, Command, Cwd}` (sanitize names/paths before display).
- `Adopt(ctx, AdoptRequest{PaneID, Name}) (AdoptResult, error)`.
- `PlanClose(ctx, CloseRequest{Name, Stop}) (ClosePlan, error)` — read-only; use it to render the confirmation
  (`ClosePlan{TaskID, Worktree, Branch, Stop, HasSession, SessionName, Identity, Origin, AgentState, Liveness}`).
- `CloseTask(ctx, CloseRequest) (CloseResult{ClosePlan, Stopped}, error)` — only pass `Stop: true` after an explicit
  confirmation. (Named `CloseTask` because `Controller.Close()` already closes the registry.)
- New error kinds: `ErrNoTmuxServer`, `ErrPaneNotFound`, `ErrPaneRegistered` (adopt.go); `ErrTaskClosed`,
  `ErrStopRefused` (close.go). Also `EnvAdoptCommands = "AGENTCTL_ADOPT_COMMANDS"`, `DefaultAdoptCommands = ["claude"]`.

**store** — `Sessions.ReconcileTransition(ctx, ReconcileInput{SessionID, From, To, Confidence, Detail, Source, Reason})`,
`Sessions.PaneOwner(ctx, domain.Identity)` (full-identity match, any state), `Sessions.OnSocket(ctx, socket)`,
`Sessions.RegisterAdopted(ctx, Task, NewSession)` (task + session + events in one tx; `ErrPaneRegistered`, `ErrTaskExists`),
`Tasks.Close(ctx, CloseInput{TaskID, StopSessionID, StopDetail})` (`ErrTaskClosed`, `ErrNotFound`).

**tmux** — `KillVerifiedSessionCommand(domain.Identity)` / `Client.KillVerifiedSession`: the only kill builder.
**adapter/claude** — `Observe` (probe errors → `Unverified` in the verdict, error always nil), `Stop` (+ `ErrStopRefused`).

**CLI** — `agentctl adopt [--all]`: `PANE SESSION:WINDOW COMMAND CWD` table, or a "No unregistered panes …" line.
`agentctl adopt %N --name foo`: `adopted %N as foo · <branch> · <worktree> · state unknown (…hooks-snippet…)`.
`agentctl close foo [--stop [--yes]]`: prompt `… [y/N] ` on stderr, only `y`/`yes` confirms; EOF = No → exit 1,
`agentctl: close --stop aborted; nothing changed`. `--yes` without `--stop` is a usage error.

## Event payloads added

- `session.disconnected {reason, from}`; it is the `cause_id` of the `agent.state_changed` it causes, which also carries `reason`.
- `task.created {…, origin: "adopted"}` and `session.adopted {socket, server_pid, session_id, session_name, pane_id, pane_pid, agent_type, command}`.
- `task.closed {stopped: bool, session_id?}` (events.go comment says `{}`).
- close --stop: `agent.state_changed {to: stopped, source: user, detail: "stopped by user (close --stop)"}` in the same tx as `task.closed`.

## Findings (tmux 3.5a, macOS)

1. **Skeleton bug, needs a fix in `internal/tmux/panes.go` (not my file):** for a pane killed by a signal, tmux prints
   `pane_dead_status` empty and `pane_dead_signal` as a *name* (`term`). `parsePane` rejects the line, so the whole
   `Snapshot` fails and every session on that server stays `unverified`: reconcile, attach, capture and delivery all
   stop working for that server once any agent dies by a signal (Ctrl-C SIGINT, OOM SIGKILL). Repro:
   `TestReconcileSignalKilledPaneIsFailed` (skipped; un-skip after the fix). Suggested fix in `parsePane`: treat a
   non-numeric field 7 as a signal name mapped through a small table (`hup 1, int 2, quit 3, abrt 6, kill 9, segv 11,
   pipe 13, term 15`, unknown names → e.g. 255), and keep empty → 0 for field 6.
2. `if-shell -F -t %N …` with a **missing** `%N` does not fail: tmux evaluates the format in some other context and
   runs the command if it is true. The kill guard therefore compares `pane_id` itself, plus server pid, session id
   and pane pid.
3. Inside a command string tmux expands `$NAME` (and `$3`) from the global environment, unquoted `$3` becomes empty.
   The guarded `kill-session -t '$3'` single-quotes the id; ids are validated as `$<digits>` / `%<digits>` first.
4. `pane_current_command` is the executable's own name: a symlink `claude -> /bin/sleep` shows `sleep`, a shell
   script shows `bash`. Claude Code's native installer symlinks `~/.local/bin/claude` into `versions/<x.y.z>`, so a
   real claude pane may report the **version string**, not `claude`. Director: check in the m3 demo
   (`tmux list-panes -a -F '#{pane_current_command}'` beside a running claude). Mitigations already in place:
   `AGENTCTL_ADOPT_COMMANDS`, `agentctl adopt --all`, and `adopt %N` accepting any live pane.

## Decisions / deviations

- The starting timeout also applies when the probe is unverified (it is about missing hooks; unknown is the honest
  label). Unverified never produces disconnected.
- A dead pane closed with `--stop` first records its exit status (`failed exit 3` stays failed), then the session is
  killed; only a non-terminal session becomes `stopped` / source `user`.
- `close --stop` refuses: no session, origin adopted, stored `disconnected`, or a live check that is not alive/dead
  (mismatch, missing, server gone, probe error). Refusals change nothing. Plain `close` always works.
- `close` of an already-closed task is an error (`ErrTaskClosed`, exit 1), not a silent no-op.
- Adopt refuses dead panes; a reused `%N` after a restart is *not* "registered" (different pids), so it is a valid
  candidate and adopting it creates a new task. The old task keeps its old identity and stays disconnected.
- Adopted base: `merge-base(HEAD, main checkout HEAD)` with `base_ref` = the main checkout's branch; fallback
  `base_ref "HEAD"`, `base_sha` = HEAD. Worktree = `git rev-parse --show-toplevel` of the pane cwd; branch = its HEAD ref.
- The registered-pane check and both inserts run in one `BEGIN IMMEDIATE` tx (`RegisterAdopted`), so concurrent adopts
  of one pane cannot double-bind it; this duplicates the INSERT column lists of `Tasks.Reserve` / `Sessions.Create`.

## Notes for other cards / the director

- **m1-hook-state:** terminal states are sticky, so a `SessionEnd` hook (→ `stopped`) that lands before reconcile sees
  `pane_dead_status 3` keeps the session `stopped`; the non-zero exit is lost. If that matters, have SessionEnd leave the
  state for reconcile when the pane is still verifiable, or let reconcile upgrade `stopped`→`failed` (needs a
  transitions change). For identity matching of adopted panes from a hook (`$TMUX` gives socket + server pid,
  `$TMUX_PANE` the pane id; no pane pid) match active sessions on (socket, server pid, pane id); `PaneOwner` needs the pane pid.
- **m2-tui-manage:** adopt picker = `ListAdoptCandidates` + `Adopt`; `x` = `PlanClose` then `CloseTask`; "close and
  stop" only when `ClosePlan.Origin == launched` and `PlanClose(Stop: true)` returns no error.
- **m3-acceptance-e2e:** AC2/AC5 building blocks: `TestRestartRecoveryFreshProcess`, `TestReconcileServerRestartReusedPaneIsIdentityMismatch`,
  `TestCloseStopRefusesIdentityMismatch`, `TestKillVerifiedSessionNeverHitsAReusedPaneAfterRestart`.
- Tests that "kill a pane" use `srv.Client().KillVerifiedSession(ctx, identity)`: archtest forbids the kill
  vocabulary in every file except `tmux/kill.go` and `claude/stop.go`, tests included.
