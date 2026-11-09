---
id: m3-repo-scope
title: List every repo by default (r = this repo) and pick the repo for a new agent
status: in-review
priority: medium
milestone: m3-phase1-acceptance
labels:
- phase-1
- ux
- user-request
created: '2026-11-09'
updated: '2026-11-09'
acceptance_criteria: '- The TUI list and `agentctl ls` show tasks from every repo
  by default, with the repo column; the header reads `agents / all repos`

  - `r` (action `repo_scope`, rebindable in config.yaml) toggles to the pane''s repo
  and back; header shows `agents / <repo>`; outside a repo `r` shows an inline "not
  in a repo" hint; the popup always opens on all repos

  - `agentctl ls --repo <path>` scopes to the repo containing <path> (`.` = current);
  `--json` scope reflects it

  - The `n` form has a repo field above name/goal: defaults to the pane''s repo (else
  the first known repo); left/right cycle known repos; typed paths expand `~`; tab
  completes directories (longest common prefix, candidates listed when ambiguous)
  and moves to the next field when nothing is left to complete; up/down/shift+tab
  still move fields

  - A path inside a repo or one of its worktrees resolves to the main repo; not-a-repo
  / no-commits refusals show inline and keep all fields

  - `agentctl new <name> --goal ... --repo <path>` (default: current repo); worktree
  stays `<repo>-worktrees/<name>`, branch `agentctl/<name>`

  - Tests first (controller, cmd, TUI golden/input); README updated; `scripts/doctor.sh`
  green'
history:
- when: '2026-11-09'
  from: null
  to: todo
- when: '2026-11-09'
  from: todo
  to: in-progress
  note: built in .tinkerbell/worktrees/m3-repo-scope
- when: '2026-11-09'
  from: in-progress
  to: in-review
  note: doctor green on branch m3-repo-scope; waiting for the user to try it before
    merge
---

# List every repo by default (r = this repo) and pick the repo for a new agent

User request (2026-11-09). Today the popup opens in the current pane's path, so the list is scoped to that repo, and `n` always launches in that repo. The user runs agents in several repos (nbagmleague, agent-tui) and wants one list across all of them, with "this repo" as a hotkey filter, and to choose the repo when starting an agent (still a separate worktree).

Agreed in chat: `r` toggles scope; the view resets to all on open; `--repo <path>` on both `ls` and `new`; tab completes directories in the repo field.
