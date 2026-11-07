---
id: m1-hook-state
title: 'Hook-driven agent state: `agentctl hook`, Claude settings generation, fakeagent'
status: in-progress
priority: medium
milestone: m1-see-agents
labels:
- spec-derived
- phase-1
dependencies:
- m1-walking-skeleton
created: '2026-11-06'
updated: '2026-11-07'
acceptance_criteria: '- Golden test of generated settings: all mapped events, command
  = shell-quoted abs path + '' hook'', timeout 5, file mode 0600, rewrite byte-identical

  - ParseHook table test covers every row of the hook->state table incl. AskUserQuestion,
  permission_prompt, elicitation_dialog, idle_prompt, PermissionRequest, SessionStart
  compact -> no change, unknown event -> recorded/no change; malformed JSON -> error

  - agentctl hook exits 0 with empty stdout in <2s (measured) for: valid payload,
  garbage, empty stdin, >1 MiB, missing home, DB write-locked by another process;
  errors appended to logs/hook-errors.log

  - Orphans: mismatched TMUX_PANE or server pid -> hook.orphan, no state change; hook
  from an adopted pane with no task env applies and sets confidence hook

  - A grep of DB + WAL bytes for a test prompt finds nothing; only prompt_sha256 stored;
  transcript_path never opened

  - fakeagent parses the generator''s real output (contract test); raw mode + \e[?2004h
  + ''> '' prompt; UserPromptSubmit -> works FAKEAGENT_WORK_MS firing Pre/PostToolUse
  -> Stop; flips to ready only after the Stop hook returns; #ask / #exit N / #sleep
  MS; SessionEnd on exit; FAKEAGENT_LOG JSONL {ts,text,state_at_receipt}; FAKEAGENT_PASTE_PLACEHOLDER=1

  - Integration (isolated tmux + fakeagent + built agentctl): new t1 goes starting->ready->working->ready
  within 5s in ls --json; #ask -> awaiting_input; send-keys y -> working -> ready;
  #exit 0 -> stopped

  - hooks-snippet prints valid JSON identical in hooks content to the generated settings

  - docs/notes/m1-hook-state.md records the payload fields relied on and how to run
  scripts/hook-probe.sh'
history:
- when: '2026-11-06'
  from: null
  to: backlog
- when: '2026-11-07'
  from: backlog
  to: in-progress
  note: 'director: dispatched to worktree /Users/jpark/Code/agent-tui/.tinkerbell/worktrees/m1-hook-state
    (tb/m1-hook-state)'
---

# Hook-driven agent state: `agentctl hook`, Claude settings generation, fakeagent

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m1-hook-state.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Why:** the riskiest product assumption — that Claude Code hooks give reliable
`working` / `awaiting_input` / `ready` state. On the critical path; learn payload
reality in m1, not m3.

**ARCHITECTURE.md sections:** Agent adapter → Hook settings + hook→state table
(note `SessionStart compact` = no change), "Hook stdout must be empty",
Identity and reconciliation (orphans, adopted-pane matching), fakeagent
contract, Invariants 6–8.

**Owns:**
- `cmd/fakeagent/*`
- `internal/adapter/claude/{settings.go, hook.go}` (rewrite stubs), `internal/adapter/claude/testdata/hooks/*.json`
- `internal/hookingest/*`
- `cmd/agentctl/{cmd_hook.go, cmd_hooks_snippet.go}` (rewrite stubs)
- `internal/store/sessions_hook.go`
- `internal/testenv/build.go` (`BuildAgentctl`, `BuildFakeAgent`, cached via sync.Once)
- `scripts/hook-probe.sh` (manual: runs real `claude` with a logging `--settings` file to capture payload fixtures — do **not** run real claude yourself; the director runs it)
- `docs/notes/m1-hook-state.md`

**Settings:** generated atomically (write tmp + rename, 0600) at
`<home>/hooks/claude-settings.json`: one command hook per mapped event pointing at
an explicit `HookCommand` (shell-quoted abs path + ` hook`; built agentctl in
tests, `os.Executable()` in prod), `timeout: 5`. `agentctl new` must ensure it
exists before launch (call the generator from the adapter's Start path — coordinate
via the stub signature, don't edit newtask.go beyond that call if the stub
already exposes it; note any needed change).

**`agentctl hook`:** read ≤1 MiB stdin under a self-timeout (HookBudget) →
resolve session: by `AGENTCTL_TASK_ID` + require `$TMUX` socket/pid and
`$TMUX_PANE` to match identity; with no task env, match an adopted session by
pane identity and upgrade confidence to `hook`; else `hook.orphan` → record
`hook.received` → `Sessions.Transition` (+ `agent_session_id`, sanitized
`last_detail`). Store prompt text as `prompt_sha256` only; never open
`transcript_path`. **Always exit 0, never write stdout**, errors to
`logs/hook-errors.log`. Leave a clear seam (interface/func var) in the pipeline
for m2-prompt-queue to add ack + deliver-trigger without restructuring.

**`agentctl hooks-snippet`:** prints the same hooks JSON for users to merge into
their own settings (how adopted panes get hooks).

Before coding, check the current Claude Code hooks reference (context7 /
docs) for 2.1.x payload field names; record what you relied on in your notes.
