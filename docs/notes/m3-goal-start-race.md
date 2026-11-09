# m3-goal-start-race — notes

## Problem

Real Claude with a goal on argv fires `SessionStart(startup)` ~230 ms before
the goal's `UserPromptSubmit`. `SessionStart` mapped to `ready`, so in that
window a `send` (or a drain) could paste into an agent about to start its goal
turn — a violation of "never paste into a working agent".

## Fix (product decision: stay `starting`)

- `adapter/claude.HoldForGoal` (pure): a `SessionStart` with `source ==
  "startup"` becomes `starting` / `submitting goal`. Everything else is
  unchanged. `resume`, `clear` (and `compact`, already record-only) keep their
  mapping.
- `hookingest.withGoalHold` applies it only when the matched session is
  `launched` and its **task row's `goal`** is non-empty. Each hook is a fresh
  process reading the registry, so the decision survives restarts. The task
  is read only for `SessionStart(startup)`. If reading it fails, the hook
  takes the lost-hook path, so the session is treated as stale and nothing is
  delivered.
- In the store, `starting → starting` is a self move: no `agent.state_changed`,
  `state_since` kept, confidence becomes `hook`, and the detail becomes
  `submitting goal`. Because nothing changed, the trigger followup spawns no
  deliver.
- Reconcile's `StartingTimeout` still fires on `starting`. When the session is
  `starting` with confidence `hook`, `SessionStart` was seen, so the detail is
  `goal never submitted: …` instead of the folder-trust hint.

## Judgment calls

- **Timeout clock.** The timeout counts from launch: a self move keeps
  `state_since`. A trust dialog answered just before the timeout can show a
  brief `unknown` ("goal never submitted") before the goal's
  `UserPromptSubmit` moves the agent to `working`. That is harmless, because
  `unknown` is never deliverable. If the timeout has already fired,
  `SessionStart` cannot move `unknown` back to `starting`. The transition is
  refused and recorded, the agent stays `unknown`, and the goal submit moves
  it to `working`.
- **Only `startup` holds.** `/clear` and `--resume` sessions are idle, not
  submitting a goal.

## fakeagent

`FAKEAGENT_GOAL_DELAY_MS` (in `cmd/fakeagent/goal.go`) sleeps between
`SessionStart` and the initial-prompt submit. During the sleep fakeagent stays
`starting`, so a paste in the gap is logged with `state_at_receipt:
"starting"`. Without an initial prompt, fakeagent still goes `ready` right
after `SessionStart`.

## Tests

- `internal/adapter/claude/goal_test.go` `TestHoldForGoal`
- `internal/hookingest/goal_test.go`: `TestSessionStartWithGoalStaysStarting`
  (no state change, no deliver spawn with a queued message; then working →
  ready + spawn), `TestSessionStartReadyWithoutPendingGoal` (no goal, adopted,
  resume, clear), `TestSessionStartWithGoalAfterTimeoutStaysUnknown`
- `internal/controller/reconcile_goal_test.go`
  `TestReconcileStartingTimeoutAfterSessionStartIsGoalNeverSubmitted`. The
  existing `TestReconcileStartingTimeoutBecomesUnknown` still covers the
  no-hook (trust or login) path.
- `cmd/fakeagent` `TestGoalDelayKeepsStartingUntilSubmit` and
  `TestParseConfig`
- `internal/e2e/goalstart_test.go`:
  - `TestGoalStartRaceQueuesSend` uses a 4 s gap. It sends inside the gap and
    expects `queued · agent starting` with no paste. It then checks that the
    message is acknowledged only after the goal turn, that the fakeagent log
    holds exactly one paste received while `ready`, and that there is no
    `starting>ready` transition.
  - `TestNoGoalStillReadyOnSessionStart`.

## demo.sh

Step 1's wait now uses `first_turn_done`: the agent must be ready, and its
detail must not be `session started…`. A ready that came from `SessionStart`
can therefore never satisfy the "ready after its first turn" check.
