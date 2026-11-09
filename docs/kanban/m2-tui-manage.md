---
id: m2-tui-manage
title: 'TUI manage views: diff, logs, new task, adopt, close, configurable keymap'
status: in-progress
priority: medium
milestone: m2-direct-agents
labels:
- spec-derived
- phase-1
dependencies:
- m2-tui-inbox
- m1-inspect
- m1-pane-identity
created: '2026-11-06'
updated: '2026-11-08'
acceptance_criteria: "- Detail shows diff summary + last ~10 output lines loaded async\
  \ ('loading\u2026' placeholder; Update never blocks >50ms)\n- d -> file list; enter\
  \ opens the pager via ExecProcess and returns to the same view; works for disconnected\
  \ tasks\n- l -> scrollable recent capture; f writes the full capture and opens it\
  \ in the pager; a mismatched pane shows 'pane unavailable'\n- n -> name + goal form\
  \ with inline slug validation; NewTask async; branch-exists / not-a-repo / claude-missing\
  \ errors inline with the form kept; on success the new row is selected as starting\n\
  - a -> adopt picker; choosing a pane + name lands the row in UNKNOWN\n- x -> 'close\
  \ <task>? y/N' (default No); 'close and stop' only for launched sessions and needs\
  \ a second explicit confirmation\n- config.yaml keys: overrides apply; unknown actions\
  \ or conflicting bindings -> one-line warning + defaults; missing file fine; malformed\
  \ YAML -> banner, no crash\n- Goldens for diff, logs, new-with-error, adopt picker,\
  \ confirm at both sizes; ui.action emitted for diff/logs/new/adopt/close"
history:
- when: '2026-11-06'
  from: null
  to: backlog
- when: '2026-11-08'
  from: backlog
  to: in-progress
  note: 'director: dispatched to worktree /Users/jpark/Code/agent-tui/.tinkerbell/worktrees/m2-tui-manage
    (tb/m2-tui-manage)'
---

# TUI manage views: diff, logs, new task, adopt, close, configurable keymap

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m2-tui-manage.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Why:** spec §4.3/§4.5 and Phase-1 acceptance 1 and 3 — start, inspect, and
manage agents without leaving the popup.

**ARCHITECTURE.md sections:** User flows, Attention classification, Invariants
1, 9, spec §4.5 ("no key silently performs a destructive action").

**Owns:**
- `internal/tui/{diff_view.go, logs_view.go, newtask_view.go, adopt_view.go, confirm_view.go, detail_inspect.go}` (rewrite stubs), `internal/tui/keys_config.go`, goldens for these views
- `internal/config/*` (`config.yaml` at `$XDG_CONFIG_HOME/agentctl/`, `keys:` only)
- a single config-load line in `cmd/agentctl/cmd_tui.go` (the only card that edits it after tui-inbox)

Every action goes through the controller asynchronously via `tea.Cmd`; Update
never blocks. Pager/full views use `tea.ExecProcess` with the m1-inspect
`FullDiffCmd`/`CaptureFull` and return to the same view.
