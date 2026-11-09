# m2-prompt-queue — notes, contracts, deviations

Safe deliver-or-queue prompts with acks: `agentctl send [--cancel]`, hook-triggered `agentctl deliver`, the TUI
composer (`p`) and live messages in the detail, plus lost-hook safety. Gate: `scripts/doctor.sh`. No new modules, no
migrations (bubbles `textarea` was already pinned on main and is now used).

## What exists

| Layer | API |
|---|---|
| domain (pure) | `CanDeliver(DeliveryCheck{State, Confidence, Liveness, StaleHook}) (ok, label)`, `QueueLabel`, `SendRejected`, `ValidateMessageBody` (non-empty, ≤64 KiB, no NUL, **no ESC**, valid UTF-8), `MatchAck(AckInput) AckDecision`, lost-hook keys `LostHook{Session,Task,Pane}Key`, `LostHookKeys`, labels `Label*`, `SourceLostHook` |
| store `messages.go` | `Messages().Enqueue / Get / ForTask / ForSession / QueuedSessions / HasQueued / Claim / Revert / MarkSent / Acknowledge / AckFromHook / MarkUnconfirmed / Cancel / SweepStale / MessageEventID / ReadyCause / SubmitsSince`, `BodyHash`, `ErrMessageState`. Every state change is a conditional UPDATE guarded by the old state + `domain.CanTransitionMessage`, with its `message.*` event in the same tx. Bodies never go into event payloads. |
| tmux `buffer.go` | `LoadPasteCommands` (load-buffer `-` + `if-shell <identity guard> 'paste-buffer -p -d …' 'delete-buffer …'`), `SendEnterCommand` (same guard), `ListBuffers`, `DeleteBuffer`, `ErrPasteRefused`, `BufferName(id)` |
| adapter `claude/deliver.go` | `Deliver`: guarded paste → `PasteEnterDelay` (`WithPasteEnterDelay(ctx, d)`) → guarded Enter. Error contract: wraps `adapter.ErrNotReady` ⇒ nothing pasted; anything else (incl. `ErrNotSubmitted`) ⇒ maybe pasted, never retry |
| controller | `Send(SendRequest{TaskID, Body, Source})`, `Drain`, `DrainAll`, `DeliverAfterHook(DeliverRequest)`, `Messages(taskID)`, `Message(id)`, `CancelMessage(id)`, `InjectDrainFault` (tests: `DrainAfterClaim`, `DrainAfterPaste`), `LostHookDir`; errors `ErrInvalidMessage`, `ErrSendRejected`, `ErrMessageNotFound`, `ErrCancelRefused` |
| hookingest | `ack.go` (followup 10), `trigger.go` (followup 20), `lost.go` (marker write/clear) |
| cmd | `send <name> <message…>` (`-` = stdin) / `send --cancel <id>`; `deliver --session N [--after-pid P]` |
| tui | `compose.go` (textarea composer), `detail_messages.go` (rows + cancel), `Model.forward` fix in `app.go` |

## Delivery, as built

- **Claim** (`claimSQL`): one UPDATE … RETURNING under BEGIN IMMEDIATE: oldest queued, session `ready`+`hook`, no
  message `delivering`, none with `sent_at ≥ state_since`. Blocking on `delivering` too is needed: a claimed-but-not-yet-sent
  message has no `sent_at`, so the literal rule alone would let a second drainer claim.
- **Drain**: sweep stale → stored-state precheck (incl. lost-hook marker) → claim (cause = `ReadyCause`, the hook.received
  behind the newest `→ready`) → re-check from the DB (same `state_since` as at the claim) + `liveSession` + marker →
  adapter paste/Enter → `MarkSent` → ack any `UserPromptSubmit` that raced `MarkSent` (`SubmitsSince`). Refused paste ⇒
  revert to queued; uncertain ⇒ `unconfirmed`. Closed tasks are never drained.
- **In-tmux guard**: paste and Enter run only if `pid`, `pane_id`, `pane_pid` match and `pane_dead`, `pane_in_mode`
  (copy mode would eat the Enter), `pane_input_off` are 0. The refused branch deletes the buffer, so
  `list-buffers` is empty after every outcome.
- **Sweep / lease**: `delivering` older than `AckWindow` (a crashed deliverer) and `sent` older than `AckWindow` become
  `unconfirmed`, cause = their own claimed / sent event. They are never re-queued.
- **Acks**: hook followup `AckFromHook` (hash vs `sent`/`unconfirmed`, else timing vs `sent` within `[claimed_at,
  sent_at + AckWindow]`), plus `deliver`'s poll loop over `SubmitsSince` for the race. `deliver` marks `unconfirmed`
  after `AckWindow`.
- **cause_id**: claimed / sent → the hook.received of the ready transition; acknowledged → the UserPromptSubmit
  hook.received; unconfirmed → the message's sent (or claimed) event, whose cause is the hook. **queued and cancelled
  have no cause_id**: a user action triggers them, not a hook.
- **deliver timing knobs** (env, Go durations; tests only): `AGENTCTL_ACK_WINDOW`, `AGENTCTL_DELIVER_SETTLE`,
  `AGENTCTL_PASTE_ENTER_DELAY`. hookingest keeps its own `AckWindow` constant (15 s) for timing acks.

## Lost-hook safety, as built

- `agentctl hook` writes `<home>/hooks/lost/<key>` (`session-N`, else `task-<id>`, else `pane-N`; temp + rename,
  0600, JSON `{ts_ms, event, task, session, pane, error}`) when it parsed a managed hook but could not record it:
  payload/parse error, registry open, resolve, `RecordHook` failure (SQLITE_BUSY past the busy timeout), or the
  budget expiring before the record (`Run`'s timeout path). Never creates the home. Orphans and unmanaged panes write nothing.
- The controller compares the newest marker of a session's three keys (`ts_ms`, else mtime) with `state_since`
  (strictly newer = stale). Drain/Send report `state stale — attach to deliver`; `Reconcile` downgrades the session to
  `unknown`/`none`, source `lost_hook`.
- Cleared by the next hook for that session that is **recorded with a state** (`res.Applied && obs.State != ""`).
  Deviation from "the next recorded hook": a record-only event (e.g. SubagentStop) proves nothing about the turn,
  so it does not clear the marker.

## Deviations / cross-card edits (director: please review at merge)

1. **`internal/domain/transitions.go` + `domain_test.go`**: `ready`, `working` and `awaiting_input` may now go to `unknown`.
   This is required by the card's mandatory "Reconcile downgrades such a session to unknown/none". Hooks never produce
   `unknown`, and reconcile only does it with a From guard, so nothing else changes. `TestUnknownAcceptsHookStates` now
   asserts the new moves instead of "ready → unknown not allowed".
2. **`internal/controller/reconcile.go`**: one line calls `downgradeLostHooks` (defined in my `drain.go`), plus a doc bullet.
3. **`internal/hookingest/hookingest.go`**: minimal wiring for the lost-hook marker. A `lost` field on `runner`, the
   post-parse failures use `failLost` instead of `fail`, `recorded(...)` follows `RecordHook`, and the timeout path calls
   `lostOnTimeout`. The card designates this file as the hookingest pipeline seam.
4. **`internal/tui/app.go`** (`forward`, the documented exception): non-input messages also reach the detail when a view
   is on top. Two tests in other cards' files changed for this reason:
   - `manage_test.go` `TestDetailInspectRetriesALostResult` used to pin the lost-result bug. It now asserts the
     result survives, and still covers the retry by dropping the command.
   - `app_test.go` `TestStubKeysSayComingIn` → `TestNoStubKeysLeft`, because `p` was the last stub.
   - `comingIn` in `view.go` is now unused but kept.
   - No existing golden changed.
5. Message bodies with ESC are rejected (not in the spec list). An embedded `ESC[201~` would end the bracketed paste
   and type the rest as keystrokes.
6. **No bracketed-paste guard exists in tmux** (there is no format for "app enabled bracketed paste"). A multi-line body
   pasted into an app without it would submit line by line. We only paste into hook-confirmed `ready` Claude sessions,
   which enable it.

## Acceptance → tests

| Bullet | Tests |
|---|---|
| CanDeliver table | `domain.TestCanDeliverTable`, `TestCanDeliverNeverIntoAwaitingInput` |
| 20 concurrent drains (goroutines + 2 processes) | `main.TestConcurrentDrainsGoroutinesAndProcesses`, `controller.TestDrainConcurrentGoroutines`, `store.TestClaimConcurrentGoroutinesAndStores` |
| WORK_MS=3000 queued → sent → acknowledged(hash), nothing while not ready | `main.TestPromptQueuedWhileWorkingDeliveredOnStop` |
| #ask | `main.TestPromptNeverPastedIntoAPermissionDialog` |
| unknown/adopted | `main.TestUnknownAdoptedSessionNeverPasted`, `controller.TestUnknownSessionQueuesAttachToDeliver` |
| send to stopped/failed/disconnected rejected | `controller.TestSendRejectsTerminalAgentsAndStoresNothing`, `main.TestSendRejectsStoppedAgentAndCancelsQueuedOnly` |
| placeholder timing ack; no submit → unconfirmed, NEEDS YOU, never re-pasted | `main.TestPlaceholderPasteAcknowledgedByTiming`, `main.TestUnacknowledgedPasteBecomesUnconfirmedAndIsNeverRepasted`, `controller.TestDeliverAfterHookTimesOutToUnconfirmedAndNeverRepastes` |
| fault seams; flip to working between claim and paste | `controller.TestFaultAfterClaimAndAfterPaste`, `TestStateFlipBetweenClaimAndPasteReverts` |
| list-buffers empty; empty / >64 KiB / NUL rejected; multi-line kept | `tmux.TestLoadPasteEnterIntegration`, `TestPasteRefusedForAnotherIdentityOrCopyMode`, `controller.TestSendValidatesTheBody`, `TestSendToReadyDeliversOnceThenWaitsForTheNextStop` |
| hook < 2 s; deliver exit 0 + deliver.log; cancel queued only | `checkHooksFailedOpen` (strict e2e rigs), deliver.log asserts in the e2e tests, `TestSendRejectsStoppedAgentAndCancelsQueuedOnly`, `controller.TestCancelQueuedOnlyAndMessagesView` |
| TUI composer / messages / goldens / ui.action{prompt} | `tui.TestComposerSendsQueuesAndTheDetailFollowsLive`, `TestComposerRefusalStaysInlineAndEscCancels`, `TestMessagesLoadedWhileAViewIsOpenReachTheDetail`, `TestPromptGolden` (compose, detail_messages × 80x24 / 120x40) |
| cause_id | `main.TestPromptQueuedWhileWorkingDeliveredOnStop` (claimed/sent/acknowledged → hook.received), `controller.TestFaultAfterClaimAndAfterPaste` (unconfirmed has a cause) |
| lost-hook safety | `hookingest.TestLostHookMarker` (busy timeout + budget), `controller.TestLostHookMarkerBlocksDeliveryAndReconcileDowngrades`, `main.TestLostHookMarkerBlocksDeliveryEndToEnd` |

## Risks / follow-ups

- `send` from the CLI uses default tunables, and its sweep uses the 15 s AckWindow. Only `deliver` reads the env knobs.
- The `delivering` lease reuses `AckWindow`. A deliverer stalled longer than that would see its message swept to
  `unconfirmed` and its `MarkSent` refused. That is safe: it is never re-pasted.
- Timing acks can match a prompt the user typed themselves within 15 s of a paste (as specified).
- m3 demo: confirm Claude Code enables bracketed paste in every state where we paste, and check whether
  `UserPromptSubmit.prompt` carries the paste or a placeholder.
