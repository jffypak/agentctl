---
id: m1-inspect
title: 'Inspect: diff summary vs base, full diff in pager, recent + full output capture'
status: backlog
priority: medium
milestone: m1-see-agents
labels:
- spec-derived
- phase-1
dependencies:
- m1-walking-skeleton
created: '2026-11-06'
updated: '2026-11-06'
acceptance_criteria: '- -z numstat parser handles renames, binaries (-\t-), and paths
  with spaces/newlines (fixtures)

  - Integration summary includes committed-on-branch, staged, unstaged, untracked
  (with line counts) and lists conflicts

  - diff t pipes `git diff <base_sha>` + untracked as new-file diffs to the pager
  on a tty, plain stdout otherwise; --no-index exit 1 treated as success; --stat prints
  the summary

  - show t prints the task/session/identity/state block + last 40 non-empty lines;
  a pane emitting OSC 52 / CSI yields none of those sequences in output

  - show --full writes logs/<task>/<UTC>.log (file 0600, dir 0700) and prints the
  path

  - Disconnected or reused %N: diff still shown + ''pane unavailable (disconnected:
  <reason>)'', the reused pane is never captured; a dead (failed) pane still captures
  its final output

  - Read-only: .git/index mtime/hash, HEAD and worktree unchanged after show/diff;
  no add -N; GIT_OPTIONAL_LOCKS=0 set'
history:
- when: '2026-11-06'
  from: null
  to: backlog
---

# Inspect: diff summary vs base, full diff in pager, recent + full output capture

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m1-inspect.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Why:** spec Phase-1 acceptance 3 — view recent output and diff without hunting
tmux. Read-only by construction.

**ARCHITECTURE.md sections:** Identity and reconciliation (verify before
capture; sanitize), Agent adapter → Launch (`GIT_OPTIONAL_LOCKS=0`), Invariants 5, 9.

**Owns:**
- `internal/gitx/{diff.go, status.go}`
- `internal/tmux/capture.go`
- `internal/controller/inspect.go`
- `internal/pager/*`
- `cmd/agentctl/{cmd_show.go, cmd_diff.go}` (rewrite stubs)
- `docs/notes/m1-inspect.md`

`controller.Inspect(task)` → identity check via `liveSession` first → diff summary
vs `base_sha` (committed-on-branch + staged + unstaged + untracked + conflicts)
and the last N sanitized capture lines. Expose `FullDiffCmd(task) *exec.Cmd` and
`CaptureFull(task) (logPath)` so the TUI can later use `tea.ExecProcess`.
Pager resolution: `$AGENTCTL_PAGER` → `$PAGER` → `less -R`; plain stdout when not a tty.
