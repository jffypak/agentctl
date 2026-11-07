---
id: m2-tui-inbox
title: 'Attention-first TUI: list, detail, attach/focus, empty/error states, `stats`,
  `tmux-binding`'
status: in-progress
priority: medium
milestone: m2-direct-agents
labels:
- spec-derived
- phase-1
dependencies:
- m1-walking-skeleton
created: '2026-11-06'
updated: '2026-11-07'
acceptance_criteria: "- Header `agents / <repo> \xB7 N running \xB7 M need you` (`all\
  \ repos` outside a repo); sections NEEDS YOU / IDLE / RUNNING / UNKNOWN in order,\
  \ oldest state_since first; closed hidden, `/closed` shows them\n- Keys: j/k + arrows\
  \ move; / filters on name/goal/branch, esc clears; enter -> detail (goal, last detail,\
  \ agent type, origin, worktree, branch, base, session/pane, state+confidence+since,\
  \ needs-you reason); enter in detail attaches; q/esc back or quit\n- Control-mode\
  \ integration: after attach, list-clients -F '#{client_session}' shows the target\
  \ session; attach to a disconnected/mismatched task is refused with a one-line message\
  \ and makes no tmux call\n- A state change written by another process appears within\
  \ 1s (teatest wait-for, no sleeps)\n- Quitting via q, ctrl+c or SIGHUP leaves every\
  \ session alive (has-session)\n- Empty state text matches ARCHITECTURE.md + the\
  \ binding hint; one-line error banners (tmux missing, DB unopenable); a seeded random\
  \ 500-key sequence never panics\n- UNKNOWN rows faint and never worded as ready/idle/done;\
  \ stopped shown as 'exited'; NEEDS YOU uses the accent; NO_COLOR respected; no spinners\n\
  - stats (text + --json) over synthetic events: median open->first action + n, runs\
  \ with no action, attaches per prompt\n- tmux-binding prints `bind-key a display-popup\
  \ -E -w 80% -h 75% -d '#{pane_current_path}' '<abs agentctl>'`, the same string\
  \ the empty state shows\n- teatest goldens at 80x24 and 120x40: empty, mixed-attention\
  \ (every state, an unconfirmed message, queued-on-unknown), detail, error banner,\
  \ filter; fixed clock + color profile\n- p/d/l/n/a/x show 'coming in <issue-id>'\
  \ from the stubs"
history:
- when: '2026-11-06'
  from: null
  to: todo
- when: '2026-11-07'
  from: todo
  to: in-progress
  note: 'director: dispatched to worktree /Users/jpark/Code/agent-tui/.tinkerbell/worktrees/m2-tui-inbox
    (tb/m2-tui-inbox)'
---

# Attention-first TUI: list, detail, attach/focus, empty/error states, `stats`, `tmux-binding`

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m2-tui-inbox.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Why:** the product surface — the one-minute check-in. Depends only on the
skeleton: it renders from seeded DB fixtures covering every state; real states
arrive at merge with no code change. Runs in parallel with W1 backend cards.

**ARCHITECTURE.md sections:** Attention classification, User flows (check-in,
empty, error), Instrumentation, Invariants 1, 2, 6, Testing → TUI.

**Owns:**
- `internal/tui/*` — app shell, list, detail, header, footer, filter, `keys.go` (default keymap for **all** Phase-1 keys), styles; **stubs with final constructor signatures** for `compose.go`, `detail_messages.go`, `diff_view.go`, `logs_view.go`, `newtask_view.go`, `adopt_view.go`, `confirm_view.go`, `detail_inspect.go`; `internal/tui/testdata/*.golden`
- `cmd/agentctl/{cmd_tui.go, cmd_stats.go, cmd_tmux_binding.go}` (rewrite stubs; bare `agentctl` runs the TUI)
- `internal/controller/{attach.go, uievents.go, stats.go}`, `internal/store/events_stats.go`
- `internal/tmux/{attach.go, binding.go}`, `internal/testenv/ctlclient.go` (control-mode client via `tmux -C attach`)

Bubble Tea v2 (`charm.land/*/v2` — check the v2 API via context7 before
writing). TUI holds a concrete `*controller.Controller` (no Backend interface);
tests use a real controller on a seeded temp DB + recording tmux Runner. Poll
`EventsSince` every Poll (300ms); call `Reconcile` + `DrainAll` every Reconcile
(2s) — stubs until W2 merges. Attach: `liveSession` first; same server →
`switch-client -t <pane>` then exit; otherwise `tea.ExecProcess(tmux -S sock
attach -t sess)` with TMUX unset. `ui.opened/closed/action` carry `ui_run_id`.
Design: minimal, compact, attention-first per spec §4.2 — semantic color only,
no animation/spinners, adaptive light/dark, `NO_COLOR` respected.
