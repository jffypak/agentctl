---
id: m2-prompt-queue
title: 'Safe deliver-or-queue prompts with acks: `send`, hook-triggered `deliver`,
  TUI composer'
status: backlog
priority: medium
milestone: m2-direct-agents
labels:
- spec-derived
- phase-1
dependencies:
- m1-hook-state
- m2-tui-inbox
created: '2026-11-06'
updated: '2026-11-06'
acceptance_criteria: "- CanDeliver table: only ready + hook confidence + alive passes;\
  \ every other combination returns its label ('agent working', 'awaiting input',\
  \ 'attach to deliver', 'disconnected')\n- 20 concurrent Drain calls (goroutines\
  \ + 2 processes) on one ready session -> exactly 1 paste in the fakeagent log and\
  \ 1 message sent; the second message waits for the next Stop\n- FAKEAGENT_WORK_MS=3000:\
  \ send during working -> 'queued \xB7 agent working'; fakeagent log has 0 entries\
  \ with state_at_receipt != ready; after Stop -> sent -> acknowledged(hash)\n- #ask:\
  \ nothing pasted while the dialog is up; delivered only after y + Stop\n- Unknown/adopted\
  \ session: queued with 'attach to deliver', never pasted across 3 drain ticks, appears\
  \ in NEEDS YOU\n- Send to stopped/failed/disconnected is rejected and nothing is\
  \ stored\n- Placeholder mode -> acknowledged(timing); no submit -> unconfirmed after\
  \ a (shortened) AckWindow, shown in NEEDS YOU, and later Stops never re-paste it\n\
  - Fault seams after claim and after paste (before MarkSent) -> next drain marks\
  \ unconfirmed, no second paste; state flips to working between claim and paste ->\
  \ reverts to queued, nothing pasted\n- list-buffers empty after delivery; empty,\
  \ >64 KiB and NUL bodies rejected; multi-line bodies preserved\n- Hook still returns\
  \ <2s while deliver waits for the ack; deliver always exits 0 and logs to logs/deliver.log;\
  \ send --cancel <id> cancels queued only\n- TUI: p opens composer, ctrl+s sends,\
  \ esc cancels; message states update live in detail with a cancel key for queued;\
  \ goldens for composer + messages-in-detail at both sizes; emits ui.action{prompt}\n\
  - message.{queued,claimed,sent,acknowledged,unconfirmed,cancelled} events carry\
  \ cause_id linking the triggering hook event"
history:
- when: '2026-11-06'
  from: null
  to: backlog
---

# Safe deliver-or-queue prompts with acks: `send`, hook-triggered `deliver`, TUI composer

> **Read `docs/ARCHITECTURE.md` in full before starting** — dispatch prompts clip it at 4000 chars, and the sections named below are the contract. Obey §Code conventions & file ownership: edit only the files this card owns, rewrite owned stubs whole, put findings in `docs/notes/m2-prompt-queue.md`, commit on the worktree branch at each checkpoint, and finish with `scripts/doctor.sh` green.

**Why:** spec Phase-1 acceptance 4 — a message queued while the agent is busy is
never injected unexpectedly. The largest correctness card; the full vertical
"send a prompt" flow (CLI + TUI).

**ARCHITECTURE.md sections:** Agent adapter → Send + Delivery (rules 1–5, the
`sent_at ≥ state_since` claim rule, deliver lifecycle), Invariants 3, 4,
fakeagent contract (placeholder mode).

**Owns:**
- `internal/domain/{delivery.go, ack.go}` (`CanDeliver`, `MatchAck` — pure)
- `internal/store/messages.go` (all message state changes = conditional UPDATEs here)
- `internal/tmux/buffer.go` (load-buffer / paste-buffer -p -d / send-keys Enter / list-buffers)
- `internal/adapter/claude/deliver.go` (rewrite stub)
- `internal/controller/{send.go, drain.go (rewrite stub), messages.go, deliver.go}`
- `internal/hookingest/{ack.go, trigger.go}` + wiring into the hookingest pipeline seam (the only card that edits hookingest after m1-hook-state)
- `cmd/agentctl/{cmd_send.go, cmd_deliver.go}` (rewrite stubs)
- `internal/tui/{compose.go, detail_messages.go}` (rewrite stubs) + their goldens

**Drain:** sweep stale `delivering`/`sent` → `unconfirmed`; claim oldest queued
with one conditional UPDATE under BEGIN IMMEDIATE (incl. the one-per-ready rule);
re-check DB state + live identity, revert to `queued` on any failure; then
`load-buffer -b agentctl-msg-<id> -` → `paste-buffer -p -d` → PasteEnterDelay →
`send-keys Enter` → `sent`. **Hook side:** on transition to ready (Stop,
idle_prompt) with queued messages, spawn detached `agentctl deliver --session N
--after-pid <pid>` via the injected Spawner; on UserPromptSubmit ack by hash,
else by timing within AckWindow. **deliver:** wait hook pid exit → DeliverSettle
→ Drain → wait up to AckWindow → `unconfirmed` if no ack; always exit 0; log to
`logs/deliver.log`.
