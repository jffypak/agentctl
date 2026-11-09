---
id: m3-goal-start-race
title: Don't report ready between SessionStart and the goal's first UserPromptSubmit
status: in-progress
priority: high
milestone: m3-phase1-acceptance
labels:
- phase-1
- demo-finding
- reliability
created: '2026-11-09'
updated: '2026-11-09'
acceptance_criteria: '- A launched task with a goal stays non-deliverable from SessionStart
  until its goal turn''s Stop: a message sent in that window is queued, never pasted

  - A task launched without a goal, and an adopted task, still go ready on SessionStart

  - fakeagent can reproduce the gap (e.g. a delay between SessionStart and the initial-prompt
  submit); a test sends in that gap and asserts from the fakeagent log that nothing
  was pasted

  - scripts/demo.sh''s ''ready after its first turn'' wait no longer passes on the
  SessionStart ready (it sees the goal turn''s Stop)

  - ARCHITECTURE.md hook table updated; go test -race ./... green'
history:
- when: '2026-11-09'
  from: null
  to: todo
- when: '2026-11-09'
  from: todo
  to: in-progress
  note: 'director: dispatched in worktree /Users/jpark/Code/agent-tui/.tinkerbell/worktrees/m3-goal-start-race'
---

# Don't report ready between SessionStart and the goal's first UserPromptSubmit

> **Read `docs/ARCHITECTURE.md` (§Agent adapter, §Open questions) first.** Put notes in `docs/notes/m3-goal-start-race.md`, commit at checkpoints, finish with `scripts/doctor.sh` green.

**Why:** the real two-Claude demo (2026-11-09) showed that with a goal on argv, Claude fires `SessionStart` (mapped to `ready`) about 230 ms before the goal's `UserPromptSubmit` (`working`). Event log from alpha: `SessionStart` at …692951 → `starting>ready`, `UserPromptSubmit` at …693184 → `ready>working`. In that window the agent passes every delivery check, so a `send` issued right after `new` (or a drain triggered by the SessionStart hook) could paste into an agent that is about to start its goal turn. That breaks the "never paste into a working agent" invariant.

It also made the demo's step-1 check a false positive: `settled` saw the SessionStart `ready`, and step 2's sends came back `queued · agent working` (the queue then did the right thing).

**Direction (not prescriptive):** when the task was launched with a goal, map `SessionStart(startup)` to a non-deliverable state (stay `starting`, or `working` with detail "submitting goal") and let the goal's `Stop` produce the first `ready`. Mind `StartingTimeout`: an agent that stays on a trust/login dialog must still surface as `unknown`.
