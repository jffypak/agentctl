# codex-support: real Codex check

Date: 2026-11-12 · Codex CLI 0.167.2 (macOS, tmux 3.6a) · branch `codex-support`
Spec: `docs/superpowers/specs/2026-11-12-codex-support-design.md` (open questions 1–6).
Run with the user in a scratch git repo; the user's real registry was backed up
first (`~/.local/state/agentctl/backup-pre-codex-*`) and upgraded to schema 1000.

## Results

| # | Question | Result |
|---|---|---|
| 1 | Do `-c` hooks get persistent trust? | **Yes.** The user trusted once; later `agentctl codex` runs and `agentctl new --agent codex` launches showed no trust screen. Trust is keyed by the hook command: changing only the `timeout` did not re-prompt; a different agentctl path did ("9 hooks are new or changed"). No `~/.codex/hooks.json` fallback is needed. |
| 2 | Does `PermissionRequest` fire, and what follows approval? | **Fires.** `probe` went to NEEDS YOU `permission request: Bash`. With the user's `approvals_reviewer = "auto_review"`, Codex's reviewer approved by itself after ~3.6 s (no prompt shown to the user); `PostToolUse` → working, `Stop` → ready. A send queued during the wait stayed `queued · awaiting input` and was delivered only after `Stop`. Users with the auto-reviewer see a short NEEDS YOU that clears by itself. |
| 3 | `Interrupt` vs `Stop` after Esc | **Only `Interrupt`; no `Stop`.** Mapping `Interrupt` → ready is required (otherwise the agent stays working). Ctrl-C to quit fires `Interrupt` + `SessionEnd`, and Codex exits with code 0 (`stopped`). |
| 4 | Failed turns | Not triggered in this run. |
| 5 | Paste behavior | Bracketed paste + Enter submits. A 1,720-char, 32-line prompt arrived intact; every delivery was acknowledged by **hash** ~86 ms after the paste (Codex reports the exact text; no placeholder). |
| 6 | Folder trust in a new worktree | **None.** `agentctl new --agent codex` in `../codex-check-worktrees/<task>` showed no folder-trust dialog. |

Also observed:

- Hooks under `agentctl codex` resolve through `AGENTCTL_TASK_ID` (`match: task_env`)
  in embedded mode, as designed.
- Embedded Codex also runs a hidden helper session (`SessionStart`, `Interrupt`,
  `SessionEnd` with another / empty `session_id`). The foreign-session guard
  recorded them with `foreign_session` and changed nothing.
- The hook-trust screen surfaced as NEEDS YOU `hook trust: attach to answer` within
  one reconcile (1 s); agentctl typed nothing into it.
- `timeout=5` is accepted, but Codex clamps the `Interrupt` and `SessionEnd` hook
  timeouts to 3 s and warned on every launch. Those two now use 3 (fixed on the
  branch; no re-trust needed).
- Codex with no prompt sits in INITIALSTATE `waiting for first prompt · attach to
  start` until the user types; after that, sends from `agentctl send` deliver.
