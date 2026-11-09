---
id: m3-adopt-claude-match
title: 'Adopt picker: recognise real Claude panes (pane_current_command is the version
  string)'
status: todo
priority: high
milestone: m3-phase1-acceptance
labels:
- phase-1
- demo-finding
- adopt
created: '2026-11-09'
updated: '2026-11-09'
acceptance_criteria: '- With AGENTCTL_ADOPT_COMMANDS unset, a pane running real Claude
  Code (pane_current_command like 2.1.319) is listed as an adopt candidate

  - A plain shell, a pane running an unrelated version-named binary, and an already-registered
  pane are not listed

  - Matching uses the pane''s foreground process argv/executable path, not pane_current_command
  alone; the env override still works

  - Table test over the detection function plus one tmux integration test; go test
  -race ./... green'
history:
- when: '2026-11-09'
  from: null
  to: todo
---

# Adopt picker: recognise real Claude panes (pane_current_command is the version string)

> **Read `docs/ARCHITECTURE.md` (§Agent adapter, §Open questions) first.** Put notes in `docs/notes/m3-adopt-claude-match.md`, commit at checkpoints, finish with `scripts/doctor.sh` green.

**Why:** in the real demo, `#{pane_current_command}` for both Claude panes was `2.1.319`, not `claude`. Claude Code's launcher (`~/.local/bin/claude`) is a symlink into a versions directory, so the process name tmux sees is the version. `controller.DefaultAdoptCommands = ["claude"]` (internal/controller/adopt.go) therefore matches no real Claude pane, and `agentctl adopt` / the TUI adopt picker show nothing unless the user sets `AGENTCTL_ADOPT_COMMANDS=2.1.319` (which breaks on every Claude update).

**Direction:** resolve the pane's foreground process (pane_pid → its tty's foreground pgid, or `ps -o command`) and match when argv[0]'s basename is `claude` or its resolved path sits under a Claude versions directory. Keep `pane_current_command` as a cheap first pass; don't shell out per pane more than once per listing.
