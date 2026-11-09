# m3-acceptance-e2e — acceptance map, invariant map, demo, findings

Every spec §11 Phase-1 acceptance criterion and every ARCHITECTURE.md invariant is now
proven by a named test. `scripts/demo.sh` runs the two-agent demo against fakeagent
(tested) or real Claude Code (run by the director). Gate: `scripts/doctor.sh`. There
are no new modules, no migrations, and no edits outside this card's files.

## What exists

| File | What |
|---|---|
| `internal/testenv/tuidriver.go` | `TmuxServer.StartTUI(t, bin, env, TUIOptions{Session, Cwd, Width, Height, Args}) *TUI` runs the **built agentctl** in a pane of the isolated server, with remain-on-exit set in the same invocation and the env passed as `-e`. tmux sets `TMUX` itself, so attach takes the real switch-client path. `TUI.Screen`, `WaitScreen`, `WaitText` (all bounded by `TUIWait` = 30 s, condition waits), `Keys` (refuses Escape batched with other keys), `Type` (`-l`), `Select(name)` (j/k one key at a time, waiting for each redraw), `Alive`, `Hangup` (SIGHUP, like a closed popup), `WaitExit` → status, `History`. `SelectedRow(screen)`. |
| `internal/e2e/rig_test.go` | The rig: isolated server, temp repo, temp home, built agentctl + fakeagent. Each agent gets its own `FAKEAGENT_LOG` / `FAKEAGENT_HOOK_LOG`, passed through `AGENTCTL_AGENT_CMD`. Helpers: `ls --json` state waits, message and event reads, `assertNothingWhileBusy`, `paneAlive(identity)`. Cleanup runs `checkHooksFailedOpen` (invariant 7) on **every** e2e test and waits for detached `deliver` processes. |
| `internal/e2e/ac{1..5}_test.go` | One test per acceptance criterion (below). |
| `internal/e2e/invariants_test.go` | Fills the two invariant gaps that had no end-to-end proof (6, 9). |
| `internal/e2e/demo_test.go` | Runs `DEMO_AGENT=fakeagent scripts/demo.sh` for real. Also a static check that every tmux call in the script names the demo socket. |
| `internal/e2e/readme_test.go` | Checks the quickstart order and that the troubleshooting table quotes every error state's real message. |
| `scripts/demo.sh`, `README.md` | See below. |

## Acceptance → tests

| Bullet | Test(s) |
|---|---|
| AC1 two agents, switch from the TUI | `TestAC1_TwoAgentsSwitchFromTUI`: both agents are created from the TUI's `n` form. The test checks separate worktrees and branches, checked out. A control-mode client on `home` is moved to alpha, then beta, then alpha, each from a fresh TUI (select with j/k, ↵, ↵). Each switch is confirmed by `list-clients` (session **and** pane id), the TUI exits 0, both agents stay alive, and `stats --json` counts 3 attaches and reports a median. |
| AC2 close TUI, agents survive, reopen recovers | `TestAC2_CloseTUIAgentsSurviveReopenRecovers`: the TUI is closed with `q` and with SIGHUP, and each time the identity triple of both agents is unchanged. In a fresh TUI the detail shows the launch identity (`$S %P · pane pid N`). A prompt composed there is acknowledged (hash) and lands in alpha only. ui.opened = ui.closed = 3. |
| AC3 output + diff, return, client unchanged | `TestAC3_RecentOutputAndDiffWithoutLeavingTUI`: each agent edits README.md, writes `<name>-notes.md` and prints `MARKER-<name>`. For each agent: the detail shows `diff 1 file +1 -0 · 1 untracked` and the marker; `l` shows its own output and never the other agent's; `d` lists its own files; esc returns each time. The TUI never exits, the control client stays on `home`, and there are 4 ui.action events. |
| AC4 queued while busy, not injected | `TestAC4_QueuedWhileBusyNotInjected`. **working**: composed in the TUI while the turn runs (`will be queued · agent working`), with nothing received while not ready (fakeagent log). Exactly one paste, at `ready`, acknowledged by hash, and the claim's cause is the `Stop` hook recorded after the queue. **#ask**: queued `awaiting input`; 3 explicit `agentctl deliver` runs plus the TUI drain ticks paste nothing; the task stays in NEEDS YOU; after the user's `y` it is pasted at `ready`, its cause is again the Stop. At the end every deliver process has exited and both agents are alive. |
| AC5 stale pane disconnected, never misattributed, attach refused | `TestAC5_StalePaneShownDisconnected/killed`: the pane is killed while the TUI is open. The TUI's own tick shows `disconnected · pane_missing` in NEEDS YOU; beta stays IDLE `ready` with its identity; attach is refused (flash, TUI stays, client stays); send is rejected and stores nothing. `/reused`: the server restarts and an "intruder" fakeagent gets alpha's old `%N` and even sets `AGENTCTL_TASK_ID=alpha`. alpha and beta show `disconnected`. The list, detail, `l` and `agentctl show` never show the intruder's screen. Attach is refused, send is rejected, nothing reaches the intruder, and its hooks are orphans: alpha's state history is only `starting>ready`, `…>disconnected`. |
| Invariant map + gap tests | This file (next section); gap tests `TestInv6_…`, `TestInv9_…`, `TestDemoScriptOnlyTalksToItsOwnSocket`. |
| `go test -race -count=10 ./internal/e2e`, no sleeps, skip without tmux/git | See "Results" below. Every wait is `testenv.WaitFor` on screen, registry or log content (`stateWait`/`TUIWait` 30 s, demo 3 min). The rig and the demo test `RequireTool` tmux, git and bash, so each test skips with "<tool> not on PATH". |
| Demo hermetic + tested | `TestDemoScriptFakeagentIsHermetic`. |
| README quickstart + troubleshooting; stats median after the demo | `README.md`, `TestREADMEQuickstartAndTroubleshooting`. The demo's TUI step makes `agentctl stats` print `open → first action (median)  0.3s`, which `TestDemoScriptFakeagentIsHermetic` asserts. |

## Invariants → tests

| # | Invariant | Proven by |
|---|---|---|
| 1 | TUI is presentation only | archtest `TestRepositoryConformsToArchitecture` (rule `tui-imports`: no store/tmux/gitx/hookingest/sql in `internal/tui`) + `TestRuleSelfTests`. End to end, every TUI action in AC1–AC5 and Inv9 (new, attach, prompt, logs, diff, close) goes through the controller, and its effect is asserted only through `ls --json` and the store's read side. |
| 2 | Agent lifetime ≠ agentctl lifetime | `TestAC2_…` (q and SIGHUP, same pane pid), `TestAC4_…` (hook and deliver processes exit, agents alive), `TestInv9_…` (plain close from the TUI keeps the agent; only `close --stop --yes` ends one), `TestAC1_…` (attach exits the TUI, agents alive), `main.TestTUIQuitLeavesAgentsAlive`, archtest `kill-commands`. |
| 3 | No blind injection; never into awaiting_input | `TestAC4_…` (both cases, against the fakeagent log), `main.TestPromptNeverPastedIntoAPermissionDialog`, `main.TestUnknownAdoptedSessionNeverPasted`, `main.TestLostHookMarkerBlocksDeliveryEndToEnd`, `domain.TestCanDeliverNeverIntoAwaitingInput`, `controller.TestStateFlipBetweenClaimAndPasteReverts`, `tmux.TestPasteRefusedForAnotherIdentityOrCopyMode`, archtest `paste-sendkeys`. |
| 4 | At-most-once delivery; crash → unconfirmed, never redelivered | `controller.TestFaultAfterClaimAndAfterPaste` (crash after claim and after paste → unconfirmed, never re-pasted), `controller.TestDeliverAfterHookTimesOutToUnconfirmedAndNeverRepastes`, `main.TestUnacknowledgedPasteBecomesUnconfirmedAndIsNeverRepasted` (more Stops, exactly one paste), `main.TestConcurrentDrainsGoroutinesAndProcesses`, `store.TestClaimConcurrentGoroutinesAndStores`. The e2e tests AC2/AC4 and the demo assert exactly one paste per message. |
| 5 | Explicit identity; reuse → disconnected; capture/attach/deliver verify first | `TestAC5_…/killed` and `/reused`, `controller.TestReconcileServerRestartReusedPaneIsIdentityMismatch`, `controller.TestInspectReusedPaneShowsDiffButNeverCaptures`, `tmux_test.TestCapturePaneReusedPaneIDIsNeverCaptured`, `tmux.TestKillVerifiedSessionNeverHitsAReusedPaneAfterRestart`, `controller.TestCloseStopRefusesIdentityMismatch`, `controller.TestAttachRefusalsAreOneLineAndNeverSwitch`, `hookingest.TestOrphansChangeNothing`. |
| 6 | Unknown is honest; failed/unknown never success | **gap filled:** `TestInv6_FailedStoppedAndUnknownNeverRenderAsSuccess` (real TUI: exit 3 → NEEDS YOU `failed · exit 3`, exit 0 → IDLE `exited · exit 0`, adopted without hooks → UNKNOWN `unknown` even after it finished a turn; no row says ready/done/success/finished). Also `domain.TestUnknownNeverRendersAsSuccess`, `domain.TestAttentionUnknownDependsOnOrigin`, `domain.TestAttentionMatrix`. |
| 7 | Hooks fail open (exit 0, no stdout, < 2 s, errors logged) | `rig.checkHooksFailedOpen` runs on every e2e test: every hook run that fakeagent logged, orphans included, exited 0 with empty stdout in < 2 s, and `hook-errors.log` is empty. Also `main.checkHooksFailedOpen` (cmd integration), `hookingest.TestBudgetBoundsAStuckHook`, `TestBadInputIsLoggedNeverRecorded`, `TestMissingHomeCreatesNothing`, `TestLostHookMarker`. |
| 8 | Tests are hermetic | `testenv.TestNewEnvIsHermeticInsideTmux`, `testenv.TestTmuxServerIsIsolatedAndCleanedUp`, archtest `exec-ownership` + `kill-commands`. **Gap filled for the demo:** `TestDemoScriptOnlyTalksToItsOwnSocket` (every tmux call in demo.sh is `-L ${DEMO_SOCKET}`) and `TestDemoScriptFakeagentIsHermetic` (hermetic env; its server is gone after it exits). Every e2e test runs through `testenv` (isolated `-L`, temp HOME/XDG/AGENTCTL_HOME, no TMUX). |
| 9 | No destructive git; close only marks closed | **gap filled:** `TestInv9_CloseNeverDeletesWorktreeOrBranch` (TUI `x y` and `close --stop --yes`; uncommitted edits, worktrees and branches all kept). Also archtest `gitx-nondestructive`, `main.TestShowAndDiffAreReadOnlyAndUseOptionalLocks0`, the demo's close step. |
| 10 | Domain is pure | archtest `domain-imports` (allowlist) + self-test; `domain.TestAgentStateTransitions`, `TestTerminalStatesAreSticky`, `TestAttentionMatrix`, `TestCanDeliverTable`. |

## The demo (`scripts/demo.sh`)

`DEMO_AGENT=fakeagent|claude`. It creates a throwaway dir (`mktemp`, deleted at exit
unless `DEMO_KEEP=1`), a fresh repo `demo-repo`, a private `AGENTCTL_HOME`, and an
isolated server `tmux -L ${DEMO_SOCKET:-agentctl-demo} -f /dev/null`. It refuses to
start if that server already has sessions. The popup binding is installed **on that
server only**, with its global env pointing at the demo home. Cleanup kills only
that server and removes its socket file. fakeagent mode also isolates HOME/XDG/git
config. Claude mode keeps HOME (it needs your login) but never writes to `~/.claude`.

Steps:
1. `new alpha` and `new beta` with goals; wait for each first turn.
2. Send a 13-line, ~1 KB prompt and then a short one to alpha. Expected: `#1 sent ·
   awaiting ack`, `#2 queued · agent working`. Wait until both are acknowledged.
3. Make an edit in alpha's worktree (fakeagent only), then `diff --stat` and `show`.
4. Run the TUI in pane `demo-ui`: open, `d`, esc, `q`, exit 0.
5. `agentctl stats` must print a median.
6. Findings (needs sqlite3): `pane_current_command` per agent, hook events by name,
   state chains, messages with `ack_match`, orphan count, hook-errors.log.
7. Optional manual checklist (`DEMO_PAUSE=1`, the default for claude on a tty).
8. `close alpha --stop --yes`, `close beta`. alpha's session must be gone and beta's
   still running; worktrees and branches kept.

fakeagent mode fails at the first failed check. Claude mode turns a failed check into
`FINDING:` and goes on. The run ends with a `FINDINGS (<agent>):` block.
`DEMO_TIMEOUT` (claude default 300 s) bounds each wait.

### For the director's real run

```sh
DEMO_AGENT=claude scripts/demo.sh           # from this checkout, in or outside tmux; builds agentctl with go
```

- Each agent needs your answer at least once. The goals ask for an edit (alpha) and a
  Bash command (beta), so expect permission prompts. Each worktree may also show the
  folder-trust dialog, and **no hooks fire until it is accepted**. The script prints
  `tmux -L agentctl-demo attach -t agentctl-<name>` hints when it sees
  `awaiting_input` or `unknown`.
- Pause screen: attach to `demo-shell`, press `prefix a` (the popup), select beta,
  ↵ ↵. Does the client land on `agentctl-beta`?

### Findings template (fill from the run → ARCHITECTURE.md Open questions)

| Question | Where the demo shows it | Answer |
|---|---|---|
| Hooks reliable for working / awaiting_input / ready? | "state changes" chains; "hook events received" (expect SessionStart, UserPromptSubmit, PreToolUse/PostToolUse, Notification permission_prompt or PermissionRequest, Stop) | |
| Paste: hash or `[Pasted text #N]` (timing)? | "messages": `#1 … ack=hash\|timing` for the 1 KB paste, `#2` for the short one; a `FINDING: … acknowledged by timing` line | |
| Paste delivered and submitted at all (bracketed paste + Enter after 100 ms)? | `#1`/`#2` acknowledged vs `unconfirmed` | |
| `--settings` hooks merge with user/project hooks? | Manual check 2 (did your own hooks still run?) | |
| Popup `switch-client` lands on the agent pane? | Manual check 1 | |
| `pane_current_command` of a claude pane | "pane_current_command alpha: …" (+ FINDING if not `claude`); affects the `a` adopt picker default | |
| Orphan hooks / hook errors | "orphan hooks: N", hook-errors.log | |
| Folder-trust dialog on fresh worktrees | did alpha/beta sit in `starting` → `unknown` until trusted? | |

## Findings (this card)

1. **ESC batched with the next key reads as alt+key.** `send-keys Escape q` in one
   call left the TUI on the detail view, because Bubble Tea parsed `ESC q` as `alt+q`.
   This is the same thing a fast human typist could hit over a slow ssh link.
   `TUI.Keys` refuses to batch Escape. For product: no action needed (the terminal
   cannot tell the two apart).
2. **Ages make rows change every second.** A driver that waits for "the selected row
   changed" passes spuriously when the age column ticks. `Select` compares task names.
3. The reused-`%N` scenario holds end to end, including a hook that **claims the task**
   from the reused pane (`AGENTCTL_TASK_ID=alpha`). Those hooks are orphans and change
   nothing: the stored `disconnected` is sticky, and identity matching rejects them.
4. The plain `close` from the TUI says "the agent keeps running", and it does.
   `close --stop` from the CLI ends only that session.
5. No product bugs were found by the e2e suite.

## Deviations

- The spec's AC1 does not say how the agents are started. AC1 starts both from the TUI
  `n` form, which is stronger. The other ACs start agents with `agentctl new` so each
  can have its own fakeagent log.
- The demo reuses an **empty** existing server on `DEMO_SOCKET` instead of always
  starting one. This lets the test pre-create it through testenv (archtest keeps
  `kill-server` inside testenv, and testenv's cleanup is the backstop). A server with
  sessions is refused.
- The demo needs `sqlite3` only for the findings block. Without it, the block points
  at the DB and prints the deliver.log ack lines instead.

## Results

- `go test -race -count=10 ./internal/e2e` passed twice on the final code: 108.6 s
  outside tmux and 108.4 s inside the user's tmux (`$TMUX` set, never touched). That
  is 10 iterations × 10 tests each, all parallel.
- `scripts/doctor.sh` is green: gofmt, vet, `go test -race ./...` (e2e 18 s), and
  `go mod tidy -diff`.
- `DEMO_AGENT=fakeagent scripts/demo.sh` passes standalone on its own `-L` socket and
  leaves no socket file or temp dir behind.

## For the director

- No migrations, no go.mod/go.sum change, no files outside this card's list.
- Run `DEMO_AGENT=claude scripts/demo.sh` once and fold the findings table into
  ARCHITECTURE.md "Open questions".
