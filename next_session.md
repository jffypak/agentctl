---
updated: 2026-11-07
---

# Next session handoff

## Session summary

- `/tb:spec` turned `agent-control-spec.md` into `docs/ARCHITECTURE.md` (Phase-1
  contract, incl. planner amendments A1–A12 and file-ownership rules) and a
  12-card board (8 Phase-1 cards in waves W0–W3 + 4 backlog m4/m5 cards).
- `/tb:orchestrate` round 1: **W0 `m1-walking-skeleton` done** and merged to main
  (gate green). W1 dispatched in parallel worktrees: `m1-hook-state`,
  `m1-pane-identity`, `m1-inspect`, `m2-tui-inbox` (in-progress). They were
  interrupted by a usage limit at ~01:00 and resumed at 02:01.
- User decisions: Phase 1 only; fake agent in tests, one real two-claude demo at
  the end; module `github.com/jffypak/agentctl`; parallel waves despite the
  director partition heuristic; acceptance = architecture-doc + project
  `scripts/doctor.sh` + clean-git (pytest isn't installed). "Keep building until
  done; for questions, decide as the product-wizard persona."

## Current state

- main has the skeleton (`agentctl new` / `ls`, full schema, domain, stubs, archtest).
- W1 worktrees under `.tinkerbell/worktrees/<id>` on branches `tb/<id>`.
- Director helper: `director_ops.py` in the session scratchpad (start / accept /
  finish / redispatch / status). If it's gone, re-create it from tinkerbell
  `director` + `acceptance` APIs with `GO_CHECKS`.
- **User cleanup pending** (W0 test leak, cause fixed): tmux session
  `agentctl-demo` on the default server, worktree
  `/Users/jpark/Code/agent-tui-worktrees/demo`, branch `agentctl/demo`.

## Next session TODO

- [ ] Accept + merge each W1 card (`director_ops.py finish <id>`); fold
      `docs/notes/<id>.md` deviations into ARCHITECTURE.md.
- [ ] Run `scripts/hook-probe.sh` once against real claude (from m1-hook-state).
- [ ] W2: `m2-prompt-queue` ∥ `m2-tui-manage`.
- [ ] W3: `m3-acceptance-e2e`, then the real `DEMO_AGENT=claude scripts/demo.sh`
      and resolve ARCHITECTURE.md open questions.

## Open questions

- Claude Code hook reliability, `--settings` hook merging, paste placeholder
  (hash vs timing ack), popup `switch-client` — all verified in the m3 demo.
- Does claude accept `--` before the goal arg? (skeleton currently rejects goals
  starting with `-`.)

## Context to load

- `AGENTS.md`
- `docs/ARCHITECTURE.md`
- `docs/kanban.md`
- `docs/notes/`

## Last known good

- main @ `m1-walking-skeleton done` — `scripts/doctor.sh` green.
