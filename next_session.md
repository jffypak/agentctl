---
updated: 2026-11-09
---

# Next session handoff

## Session summary

- `/tb:spec` → `docs/ARCHITECTURE.md` + board; `/tb:orchestrate` drove Phase 1 to
  completion. **All 10 Phase-1 cards are done and merged to main** with
  `scripts/doctor.sh` green: m1-walking-skeleton, m1-hook-state, m1-pane-identity,
  m1-inspect, m1-flaky-tests, m2-tui-inbox, m2-tui-manage, m2-prompt-queue,
  m2-hardening-followups, m3-acceptance-e2e. (m1-flaky-tests and
  m2-hardening-followups were added mid-round for load-sensitive flakes.)
- Director fixes on main: tmux signal-name parsing, bidi-control sanitizing,
  StopFailure → NEEDS YOU, launched-but-silent → NEEDS YOU, bubbles textarea pin,
  drain flake follow-up.
- Product calls made as product-wizard (user-delegated): StopFailure =
  awaiting_input "turn failed: <error>"; never auto-accept Claude folder trust;
  lost-hook safety rule (no paste on stale state).

## Current state

- main: ~35k lines of Go, every Phase-1 acceptance criterion + all 10 invariants
  proven by named tests (`docs/notes/m3-acceptance-e2e.md` has the maps).
- Board: 10 done, 4 backlog (m4-*/m5-*: Phases 2–3, re-spec before dispatch).

## Next session TODO

- [ ] **Real two-Claude demo (human-in-the-loop):** in a terminal,
      `cd /Users/jpark/Code/agent-tui && DEMO_AGENT=claude DEMO_KEEP=1 scripts/demo.sh`.
      Accept the folder-trust and permission prompts in the agent panes
      (`tmux -L agentctl-demo attach -t agentctl-alpha|beta`), follow the
      checklist pause (popup `prefix a` → switch-client), then fold the FINDINGS
      block into ARCHITECTURE.md §Open questions.
- [ ] Optionally run `scripts/hook-probe.sh` with real claude to refresh payload fixtures.
- [ ] Clean up the W0 test leak (user action): tmux session `agentctl-demo` on the
      default server, worktree `/Users/jpark/Code/agent-tui-worktrees/demo`,
      branch `agentctl/demo`.
- [ ] Optional: delete now-redundant `internal/hookingest/busy.go` retryFastBusy.
- [ ] Phase 2: re-spec the m4 cards (`/tb:spec` or `/tb:plan`) once the demo
      confirms hook reliability.

## Open questions

- Hook reliability, `--settings` merge with user hooks, paste ack hash vs timing,
  popup switch-client, folder-trust frequency, claude `pane_current_command` —
  all answered by the real demo.
- Does claude accept `--` before the goal arg? (goals starting with `-` are rejected.)

## Context to load

- `AGENTS.md`
- `docs/ARCHITECTURE.md`
- `docs/kanban.md`
- `docs/notes/`
- `README.md`

## Last known good

- main @ `chore(kanban): m3-acceptance-e2e done` — `scripts/doctor.sh` green.
