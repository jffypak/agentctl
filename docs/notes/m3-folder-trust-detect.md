# m3-folder-trust-detect — notes

## What it does

Reconcile reads the screen of every session that is `starting` with
confidence `none` (launched, no hook recorded yet). If the screen shows
Claude's folder-trust dialog, the session moves to `unknown` / `none` with
detail `folder trust: attach to answer` and source `pane_text` (new
`domain.SourcePaneText`). That happens on the first poll. Before this card it
took `StartingTimeout` (30 s).

- Detection: `domain.ShowsFolderTrustDialog` (`internal/domain/foldertrust.go`).
  It sanitizes the text, drops box-drawing characters (U+2500..U+257F),
  lower-cases it, collapses whitespace (so the dialog's own word wrap can't
  split a phrase), then looks for one phrase from `folderTrustPhrases`:
  - `is this a project you created or one you trust` (Claude Code 2.1.319, real demo 2026-11-09)
  - `yes, i trust this folder` (2.x option label)
  - `do you trust the files in this folder` (1.x wording)
  Generic words like "trust" or "folder" on their own never match.
- Capture: `controller.showsFolderTrust` uses `c.capture` (liveSession, then
  the server-side identity re-check in `tmux.CapturePane`). It reads the
  visible screen only (`history 0`), keeps the last 40 non-blank lines, and
  runs once per poll, only for the pre-hook `starting` window. A capture that
  fails is not a match and is not returned as a reconcile error, because the
  pane and timeout verdicts already cover a pane that died or was reused.
- Order in `reconcileSession`: pane verdict (dead, missing, reused), then
  folder trust (only when the snapshot says the pane is alive), then the
  timeout. A dialog that is still up after 30 s therefore gets the specific
  detail, not the generic one.

## Why `unknown` (not a new state)

The product call was to reuse the launched-but-silent route. `unknown` on a
launched agent is already NEEDS YOU, it is never deliverable (Deliver needs
`ready` + `hook`, and a send queues as "attach to deliver"), and it accepts
every hook state out of it. The timeout's `From: starting` guard means the
timeout can never overwrite the trust detail. If the user declines the
dialog ("No, exit"), Claude exits. Reconcile then records the dead pane as
`failed` (fakeagent exits 1) or `stopped`. The e2e test covers this.

## Merge note for m3-goal-start-race (director, please read)

My trust state is the **same** state as the existing timeout verdict
(`unknown` / `none`), so whatever SessionStart mapping the sibling card ends
up with applies to both paths in the same way. I did not hardcode
`unknown→ready`. One gap to check at merge time:

- `domain.CanTransition(unknown, starting)` is **false**. A test,
  `TestNothingReturnsToStarting`, pins that rule. If m3-goal-start-race maps
  `SessionStart(startup)` for a goal launch to a *state write* of `starting`,
  that write is refused from `unknown`. The refusal is recorded and is not an
  error. The session then stays `unknown` (with my detail, or the timeout's)
  until the goal's `UserPromptSubmit` moves it to `working` (allowed), and
  then `Stop` moves it to `ready`. That is safe because nothing is deliverable
  in that window, but for ~230 ms the detail is stale. If the sibling wants
  `unknown → starting("submitting goal")` instead, it has to add that move to
  the transition table and relax `TestNothingReturnsToStarting`. That
  decision belongs to the transition-table owner, so I did not make it here.
- The e2e test (`TestFolderTrustDialogSurfacesAndSessionStartClearsIt`) only
  asserts `starting>unknown unknown>…`, then ready, then that the queued
  prompt is delivered while ready. It should hold under either mapping.

## fakeagent

`FAKEAGENT_TRUST_DIALOG=1` lives in `cmd/fakeagent/trust.go`. agent.go has
three small hooks into it: the config field, the gate at the top of `run()`
(before the banner and SessionStart), and the `stTrust` branch at the top of
`handle()`. While the dialog is up, no hook fires. Input is logged with
`state_at_receipt: "trust"`. `1`, `y` or Enter accepts, then the screen clears
and SessionStart follows. `2` or `n` exits 1 with no hook (Claude holds hooks
back on an untrusted folder). Tests answer with tmux send-keys. agentctl
never sends keys there.

## Not done / judgment calls

- If the user accepts the dialog but no hook ever arrives (lost hook, broken
  settings), the session keeps `folder trust: attach to answer`. It is still
  honest (`unknown`, NEEDS YOU) but the detail is stale. Re-capturing
  `unknown` sessions to refresh it would widen the capture cost, so I did not
  add it.
- The attention *label* is still "silent since launch · attach to check".
  The trust text is in the detail, which both the TUI list and `ls` show.
  Showing it in the label would need detail-aware attention in the domain,
  and that is out of scope.
- `scripts/demo.sh` (claude mode) prints the trust hint on the first `unknown`
  poll whose `show` detail is the trust detail, which is about 2 s in
  practice. Otherwise it prints a login-dialog hint.
- The trust-dialog exit code is 1 in fakeagent. The real Claude exit code for
  "No, exit" was not observed. Either code surfaces (failed → NEEDS YOU,
  stopped → IDLE "exited").
