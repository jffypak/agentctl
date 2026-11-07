---
id: m4-correction-loop-inbox
title: 'Phase 2: bounded correction loop + intervention inbox items'
status: backlog
priority: low
milestone: m4-verified-tasks
labels:
- spec-derived
- later-phase
dependencies:
- m4-task-contracts-checks
created: '2026-11-06'
updated: '2026-11-06'
acceptance_criteria: "- Re-spec before dispatch; see agent-control-spec.md \xA78.3,\
  \ \xA74.4, \xA711 Phase 2"
history:
- when: '2026-11-06'
  from: null
  to: backlog
---

# Phase 2: bounded correction loop + intervention inbox items

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m4-correction-loop-inbox.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Backlog — NOT built in the Phase-1 round.** Coarse card from spec §11; re-spec (split into slices) before dispatch. The director must not pick this up.


Correction loop (§8.3): exact failure evidence + allowed scope delivered through
the m2 prompt queue; max attempts (default 3) → one actionable blocked inbox
item; cancellation; time/token budgets. Intervention screen (§4.4); never treat
an agent's suggested next step as root cause.
