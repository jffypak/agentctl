---
id: m4-skills-approval
title: 'Phase 2: manual skills (local-testing, pr-review) + approval before merge'
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
acceptance_criteria: "- Re-spec before dispatch; see agent-control-spec.md \xA77,\
  \ \xA79, \xA711 Phase 2"
history:
- when: '2026-11-06'
  from: null
  to: backlog
---

# Phase 2: manual skills (local-testing, pr-review) + approval before merge

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m4-skills-approval.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Backlog — NOT built in the Phase-1 round.** Coarse card from spec §11; re-spec (split into slices) before dispatch. The director must not pick this up.


SKILL.md discovery + `s` palette (§7); runner contract with scoped permissions,
result envelope, provenance (§7.4–7.5); `local-testing` and `pr-review`; review
output labelled as model findings, not a deterministic pass; approval record
bound to a diff/revision hash (§9.3); nothing merges without approval.
