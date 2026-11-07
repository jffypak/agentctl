---
id: m5-workflow-graph
title: 'Phase 3: declarative workflow DAG, integration checkout, locks, graph view'
status: backlog
priority: low
milestone: m5-workflow-graph
labels:
- spec-derived
- later-phase
dependencies:
- m4-correction-loop-inbox
- m4-skills-approval
created: '2026-11-06'
updated: '2026-11-06'
acceptance_criteria: "- Re-spec before dispatch; see agent-control-spec.md \xA78,\
  \ \xA711 Phase 3"
history:
- when: '2026-11-06'
  from: null
  to: backlog
---

# Phase 3: declarative workflow DAG, integration checkout, locks, graph view

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m5-workflow-graph.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Backlog — NOT built in the Phase-1 round.** Coarse card from spec §11; re-spec (split into slices) before dispatch. The director must not pick this up.


Declarative workflows with dependency edges and parallel-ready execution (§8);
event-triggered skills deduplicated by task/revision/workflow identity;
integration worktree + cross-unit checks (§8.4); re-run only failed units;
resource locks, cancellation, provenance, restart-safe execution; text-first
graph on `g`. Revisit the no-daemon decision here.
