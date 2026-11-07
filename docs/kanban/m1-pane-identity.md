---
id: m1-pane-identity
title: Reconcile, adopt, and close by explicit pane identity (disconnected detection,
  restart recovery)
status: backlog
priority: medium
milestone: m1-see-agents
labels:
- spec-derived
- phase-1
dependencies:
- m1-walking-skeleton
created: '2026-11-06'
updated: '2026-11-06'
acceptance_criteria: '- Integration cases: pane killed -> disconnected{pane_missing};
  server killed -> disconnected{server_gone}; server restarted so %N is reused with
  different pane pid + server pid -> disconnected{identity_mismatch} and the new pane
  is never bound to any task; exit 0 -> stopped; exit 3 -> failed with detail ''exit
  3''; starting beyond (shortened) StartingTimeout -> unknown/none

  - Reconcile is idempotent (twice -> same rows, no duplicate events); terminal states
  stay put even if a matching hook arrives later

  - Restart recovery: create 2 tasks, then `agentctl ls --json` in a fresh process
  -> identical mappings + states, no tmux session killed

  - adopt (no args) lists candidates (pane id, session:window, cwd) excluding registered
  panes by identity; adopt %N --name foo registers correctly with origin adopted and
  events task.created + session.adopted

  - adopt errors are one-line and actionable: pane missing, already registered, cwd
  not in git, name taken

  - close foo hides the task (ls --closed shows it), leaves the session running and
  the worktree + branch present (verified via git worktree list / git branch)

  - close --stop requires confirmation or --yes and refuses for adopted or identity-mismatched
  sessions'
history:
- when: '2026-11-06'
  from: null
  to: backlog
---

# Reconcile, adopt, and close by explicit pane identity (disconnected detection, restart recovery)

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m1-pane-identity.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Why:** spec Phase-1 acceptance 2 and 5 — close/reopen recovers mappings, and a
stale or reused pane is shown as `disconnected`, never misattributed.

**ARCHITECTURE.md sections:** Identity and reconciliation, Agent adapter →
Launch (remain-on-exit, adopted base), Invariants 2, 5, 9.

**Owns:**
- `internal/controller/{reconcile.go (rewrite stub), adopt.go, close.go}`
- `internal/adapter/claude/{observe.go, stop.go}` (rewrite stubs)
- `internal/tmux/kill.go`
- `internal/store/{tasks_close.go, sessions_reconcile.go}`
- `cmd/agentctl/{cmd_adopt.go, cmd_close.go}` (rewrite stubs)
- `docs/notes/m1-pane-identity.md`

**Reconcile:** one `list-panes -a -F` + one server-pid read per socket →
`domain.ClassifyLiveness` + StartingTimeout → `Sessions.Transition` with source
`pane`/`timeout`; emit `session.disconnected{reason}`. Idempotent. Must be
callable from `ls` and (later) the TUI tick — wire it into `ls` via the existing
controller call site.

**Adopt:** `agentctl adopt` (no args) lists *unregistered* panes whose
`pane_current_command` is in configured agent names (default `claude`,
configurable) — a candidate list only, the user picks explicitly. `adopt %N
--name foo` registers repo/worktree/branch/base (merge-base rule), origin
`adopted`, state `unknown`/confidence `none`, identity captured now; events
`task.created`, `session.adopted`.

**Close:** marks the task closed (hidden from `ls`, visible with `--closed`);
session and worktree untouched. `--stop` kills the session only for *launched*
sessions with verified identity, after interactive confirmation or `--yes`.
