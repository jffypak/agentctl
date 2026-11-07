---
id: m4-task-contracts-checks
title: 'Phase 2: task contracts + controller-executed checks + verified gating'
status: backlog
priority: low
milestone: m4-verified-tasks
labels:
- spec-derived
- later-phase
dependencies:
- m3-acceptance-e2e
created: '2026-11-06'
updated: '2026-11-06'
acceptance_criteria: "- Re-spec before dispatch; see agent-control-spec.md \xA711\
  \ Phase 2 acceptance criteria"
history:
- when: '2026-11-06'
  from: null
  to: backlog
---

# Phase 2: task contracts + controller-executed checks + verified gating

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m4-task-contracts-checks.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Backlog — NOT built in the Phase-1 round.** Coarse card from spec §11; re-spec (split into slices) before dispatch. The director must not pick this up.


Task contract (goal, allowed files, check commands, retry budget, risk) per spec
§10.1; task state machine separate from agent state (§5.2); controller-run
checks with stored exit codes + logs as evidence; diff-scope check; a task can
become `verified` only if every required check executed and passed against the
intended revision and changed files are within scope (§11 Phase-2 AC3).
