---
id: m3-folder-trust-detect
title: Surface Claude's folder-trust dialog in NEEDS YOU within seconds (never auto-accept)
status: todo
priority: medium
milestone: m3-phase1-acceptance
labels:
- phase-1
- demo-finding
- ux
created: '2026-11-09'
updated: '2026-11-09'
acceptance_criteria: '- A starting agent whose pane shows Claude''s folder-trust dialog
  is shown in NEEDS YOU with detail ''folder trust: attach to answer'' within one
  reconcile poll, not after StartingTimeout

  - agentctl never sends keys to the dialog (product call: never auto-accept folder
  trust)

  - Detection reads sanitized captured pane text through liveSession; a false match
  can only produce a hint, never a state the delivery path treats as ready

  - fakeagent gains a directive or env flag that prints a trust-style dialog before
  SessionStart; a test covers detection and the later SessionStart clearing it

  - scripts/demo.sh claude mode prints the attach hint as soon as the dialog is detected;
  README troubleshooting line updated'
history:
- when: '2026-11-09'
  from: null
  to: todo
---

# Surface Claude's folder-trust dialog in NEEDS YOU within seconds (never auto-accept)

> **Read `docs/ARCHITECTURE.md` (§Agent adapter, §Open questions) first.** Put notes in `docs/notes/m3-folder-trust-detect.md`, commit at checkpoints, finish with `scripts/doctor.sh` green.

**Why:** every agent in the real demo stopped on Claude's "Is this a project you created or one you trust?" dialog, because each worktree is a new path. No hook fires until it is answered, so the agent sat silently in `starting` and only became `unknown` (with the folder-trust hint) after `StartingTimeout` = 30 s. The demo looked hung to the user.

The last session's product call stands: never auto-accept folder trust. The fix is fast, specific surfacing, not automation. An optional follow-up worth deciding separately: a documented opt-in for trusting `<repo>-worktrees/*` paths.
