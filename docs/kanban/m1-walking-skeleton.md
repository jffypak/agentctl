---
id: m1-walking-skeleton
title: 'Walking skeleton: `agentctl new` launches an agent in worktree+tmux; `agentctl
  ls` lists it'
status: done
priority: high
milestone: m1-see-agents
labels:
- spec-derived
- phase-1
created: '2026-11-06'
updated: '2026-11-07'
acceptance_criteria: "- scripts/check.sh fails on non-empty gofmt -l, go vet, go test\
  \ -race ./..., or a go mod tidy diff; scripts/doctor.sh runs it\n- go list -m all\
  \ shows every pin from ARCHITECTURE.md (bubbletea/bubbles/lipgloss v2, modernc sqlite,\
  \ yaml v3, x/term, teatest v2); if a pin cannot resolve, report it instead of silently\
  \ changing it\n- Domain table tests: agent-state transitions (stopped/failed/disconnected\
  \ sticky; unknown\u2192hook states allowed); message-state transitions; attention\
  \ for every agent state x message summary incl. stopped, unconfirmed, queued-on-unknown;\
  \ liveness match / pane missing / pane_pid differs / server pid differs / socket\
  \ gone / dead 0 / dead non-zero; slug rejects . : / whitespace, leading -, >40 chars\n\
  - SanitizeTerminalText strips CSI, OSC (incl. OSC 52) and C0 controls except newline/tab\
  \ (table test)\n- Archtest enforces every rule in ARCHITECTURE.md \xA7Code conventions\
  \ 6, each with a negative self-test\n- Store: DB file 0600, WAL on; reopen keeps\
  \ the applied set; a synthetic unapplied lower-numbered migration is still applied;\
  \ 8 goroutines + 2 OS processes doing concurrent appends/transitions produce zero\
  \ SQLITE_BUSY\n- Sessions.Transition rejects disallowed transitions and appends\
  \ agent.state_changed atomically\n- tmux Runner with empty socket errors; tmux and\
  \ gitx arg builders unit-tested\n- Integration (TempRepo + isolated TmuxServer):\
  \ new demo -> worktree on agentctl/demo from HEAD, session agentctl-demo with cwd\
  \ in the worktree, env vars present, remain-on-exit on, stored identity equals list-panes/display-message,\
  \ state starting, events task.created + session.started\n- Goal `$(touch pwned)\
  \ \"quoted\"` arrives as a single argv element and creates no file\n- Agent `sh\
  \ -c 'exit 3'` leaves a dead pane with pane_dead_status=3\n- new run inside a task\
  \ worktree registers against the main repo; two concurrent `new x` -> exactly one\
  \ succeeds, one worktree\n- One-line actionable error, exit 1, no partial row for:\
  \ tmux missing, git missing, not a repo, agent cmd not found, invalid slug, task\
  \ exists, branch exists, worktree path exists; tmux failure after worktree creation\
  \ -> task kept with session failed + detail, worktree kept\n- ls shows the documented\
  \ columns; ls --json schema documented in docs/notes/m1-walking-skeleton.md\n- Stub\
  \ subcommands exit 2 with `not implemented yet (<issue-id>)`\n- Suite passes when\
  \ run inside the user's tmux (testenv clears TMUX/TMUX_PANE; every call passes -L/-S)"
history:
- when: '2026-11-06'
  from: null
  to: todo
- when: '2026-11-06'
  from: todo
  to: in-progress
  note: 'director: dispatched to worktree /Users/jpark/Code/agent-tui/.tinkerbell/worktrees/m1-walking-skeleton
    (tb/m1-walking-skeleton)'
- when: '2026-11-07'
  from: in-progress
  to: done
  note: "director: done \u2014 acceptance pass green; promote to done"
---

# Walking skeleton: `agentctl new` launches an agent in worktree+tmux; `agentctl ls` lists it

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m1-walking-skeleton.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Why:** the wedge's foundation. One user-visible command proves the four
integrations everything rests on (git worktree, tmux socket + identity, SQLite
WAL, attention model) and **locks the shared interfaces** so W1's four parallel
cards only add files or rewrite stubs they own. CLI-only — no TUI.

**ARCHITECTURE.md sections:** Data model sketch (incl. Tunables), Packages
(+ import graph, pins), Agent adapter → Launch / socket resolution, Identity and
reconciliation, Invariants, Attention classification, Testing (gate + hermetic),
Code conventions & file ownership.

**Owns:**
- `go.mod`, `go.sum`, `internal/deps/pin.go` (`//go:build tools` blank imports of every pinned module), `Makefile`, `scripts/check.sh`, `scripts/doctor.sh` (execs check.sh), `.gitignore` (append build outputs; keep existing tinkerbell lines)
- `cmd/agentctl/{main.go, commands.go, cmd_new.go, cmd_ls.go}` + **stub** `cmd_<name>.go` for every other subcommand (tui, adopt, show, diff, send, close, hook, deliver, stats, hooks_snippet, tmux_binding) → exit 2 `not implemented yet (<issue-id>)`
- `internal/domain/*` — complete Phase-1 enums (agent state, confidence, message state, task state), transition tables + `CanTransition`, attention classifier (incl. `stopped`→IDLE "exited", queued-on-unknown, unconfirmed), liveness classifier, `SanitizeTerminalText`, slug rules, event-type constants
- `internal/store/{store.go, migrate.go, migrations/0001_init.sql, repos.go, tasks.go, sessions.go, events.go, overview.go}` — **full Phase-1 schema incl. `messages`**; `Sessions.Transition`; `Overview` read model (tasks + session + message counts by state); `EventsSince(id)`
- `internal/tmux/{tmux.go, session.go, panes.go, server.go}` — `Runner` interface; empty socket = error; new-session+remain-on-exit in one invocation; list-panes; server pid
- `internal/gitx/{gitx.go, repo.go, worktree.go}` — main-repo resolution via `--git-common-dir`, `worktree add`
- `internal/adapter/adapter.go`; `internal/adapter/claude/{claude.go, start.go}` + **stubs** `{settings.go, hook.go, observe.go, deliver.go, stop.go}`
- `internal/controller/{controller.go, tunables.go, errors.go, preflight.go, newtask.go, overview.go, live.go}` + **stubs** `{reconcile.go, drain.go}` (no-op); `live.go` = `liveSession(sess) (domain.Liveness, tmux.Pane)`
- `internal/paths`, `internal/testenv/{repo.go, tmux.go, home.go, wait.go}`, `internal/archtest`

**Build order (commit after each):** domain → store → tmux/gitx → adapter start →
controller → cmd → archtest. On redispatch, resume from the last commit.

**`agentctl new <name> --goal "<g>"`:** validate slug → reserve task row →
resolve main repo → capture base_ref/base_sha → `git worktree add -b
agentctl/<task> ../<repo>-worktrees/<task> <base_sha>` → one tmux invocation
`new-session -d -P -F … -s agentctl-<task> -c <wt> -e AGENTCTL_TASK_ID -e
AGENTCTL_HOME -- <argv> \; set-option -w remain-on-exit on` → capture identity
(socket abs path, server pid, session id, pane id, pane pid) → session
`starting` → events `task.created`, `session.started`. argv =
`AGENTCTL_AGENT_CMD` JSON prefix (default `["claude"]`) + `--settings
<home>/hooks/claude-settings.json <goal>` (the settings file itself is written by
m1-hook-state; here just pass the path). Tests use `["sh","-c","exec sleep 600","agent"]`.

**`agentctl ls [--json] [--closed]`:** tasks grouped by attention section; scoped
to the cwd repo, or all repos outside one. Columns: name, section, state,
confidence, age, branch, worktree. `--json` = documented stable schema
(write it in `docs/notes/m1-walking-skeleton.md`).

**Not in this slice:** hooks, settings generation, fakeagent, reconcile logic,
adopt/close, diff/capture, messages logic, TUI, config loading (yaml is pinned only).
