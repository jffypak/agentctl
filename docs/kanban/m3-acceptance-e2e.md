---
id: m3-acceptance-e2e
title: Prove every Phase-1 acceptance criterion end-to-end; demo script; README
status: done
priority: medium
milestone: m3-phase1-acceptance
labels:
- spec-derived
- phase-1
dependencies:
- m2-prompt-queue
- m2-tui-manage
created: '2026-11-06'
updated: '2026-11-09'
acceptance_criteria: '- TestAC1_TwoAgentsSwitchFromTUI: two fakeagents, TUI navigation,
  control-mode client confirms each switch

  - TestAC2_CloseTUIAgentsSurviveReopenRecovers

  - TestAC3_RecentOutputAndDiffWithoutLeavingTUI: write a file in the worktree; l
  and d asserted; client session unchanged

  - TestAC4_QueuedWhileBusyNotInjected: working case and #ask case, asserted against
  the fakeagent log

  - TestAC5_StalePaneShownDisconnected: kill the pane, and separately reuse %N after
  a server restart; never misattributed; attach refused

  - docs/notes/m3-acceptance-e2e.md maps each of the 10 invariants to the test proving
  it; tests added for any gap

  - go test -race -count=10 ./internal/e2e green; no sleep-based assertions; skip
  with a reason when tmux/git missing

  - DEMO_AGENT=fakeagent scripts/demo.sh runs hermetically and is exercised by a test

  - README quickstart: install -> binding -> new -> popup -> send -> diff -> close,
  plus a troubleshooting line per error state; agentctl stats prints a median after
  the demo'
history:
- when: '2026-11-06'
  from: null
  to: todo
- when: '2026-11-09'
  from: todo
  to: in-progress
  note: 'director: dispatched to worktree /Users/jpark/Code/agent-tui/.tinkerbell/worktrees/m3-acceptance-e2e
    (tb/m3-acceptance-e2e)'
- when: '2026-11-09'
  from: in-progress
  to: done
  note: "director: done \u2014 acceptance pass green; promote to done"
---

# Prove every Phase-1 acceptance criterion end-to-end; demo script; README

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m3-acceptance-e2e.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Why:** Phase 1 isn't done until each spec §11 acceptance criterion and each
ARCHITECTURE.md invariant is proven by a named test, and real-claude behavior
is checked once.

**ARCHITECTURE.md sections:** Testing → E2E, Invariants (all), Open questions.

**Owns:**
- `internal/e2e/*`
- `internal/testenv/tuidriver.go` (runs the real `agentctl` TUI in a pane of the isolated server; `send-keys` + `capture-pane` wait-for)
- `scripts/demo.sh` (`DEMO_AGENT=fakeagent|claude`; throwaway repo; two agents), `README.md`
- `docs/notes/m3-acceptance-e2e.md` (invariant → test map; demo findings template)

Do **not** run real `claude` — the director runs `DEMO_AGENT=claude scripts/demo.sh`
once and folds findings (hook reliability, paste placeholder hash-vs-timing,
`--settings` merge, popup `switch-client`) into ARCHITECTURE.md Open questions.
