# m2-tui-inbox — notes, contracts for later cards, findings

Bare `agentctl` (or `agentctl tui`) opens the attention-first TUI; `agentctl stats [--json]` and
`agentctl tmux-binding` are real. Gate: `scripts/doctor.sh`. No new modules, no migrations.

## What exists

**internal/tui** (Bubble Tea v2, presentation only; imports controller, never store/tmux/gitx):
- `Options{Controller, OpenErr, Cwd, Binding, Keys *KeyMap, Warning, NoColor, RunID, Input, Output, Environ}`,
  `New(ctx, Options) Model`, `Run(ctx, Options, ...tea.ProgramOption) error`.
- `Run` records `ui.opened` (sets `RunID`), runs the program, records `ui.closed`. A cancelled ctx (cmd_tui cancels on
  SIGHUP) becomes a graceful `Quit` and returns nil. TMUX/TMUX_PANE are removed from the environment Bubble Tea sees.
- Loop: initial load (`LastEventID` + `Overview`), then `EventsSince` every `Tunables.Poll`, reloading the Overview when
  events arrived or ages are >1s stale; `Reconcile` + `DrainAll` every `Tunables.Reconcile`; `Preflight` once (banner).
  Exactly one poll loop runs; stale poll results never overwrite newer ones. Update never calls the controller.
- List: header = `Overview.Header()` text; sections NEEDS YOU / IDLE / RUNNING / UNKNOWN (CLOSED only via the filter),
  rows `> name  label · last detail  age`, blank lines between sections when they fit, scrolls to keep the selection.
- Filter `/`: live, case-insensitive, every term must match name, goal or branch; the word `closed` lists closed tasks
  instead of open ones; enter applies, esc clears. Arrows move while typing (j/k are text).
- Detail (enter): title + section/label, goal (≤3 wrapped lines), last, state · confidence · since, needs-you reason,
  agent · origin, worktree, branch, base @ sha7, tmux session $id %pane · pane pid, socket, then the messages and inspect
  sections. enter attaches; q/esc back.
- Styles: 256-palette accent (NEEDS YOU), red (errors), muted; light/dark via `tea.RequestBackgroundColor`; `NO_COLOR`
  keeps only bold/faint/reverse. UNKNOWN rows are faint. No spinners or animation (tested).

**internal/controller**: `Attach(ctx, taskID) (AttachResult{TaskID, SessionName, PaneID, Switched, Cmd}, error)`
(`ErrAttachRefused`, `ErrAttachFailed`, `ErrTaskNotFound`, `ErrTmuxMissing`); `TmuxBinding(agentctlPath)`;
`UIOpened(ctx) (runID)`, `UIAction(ctx, runID, action, taskID)`, `UIClosed(ctx, runID)`, `UIAction*` constants +
`UIActions()`; `Stats(ctx) (Stats{Runs, RunsWithAction, RunsWithoutAction, MedianOpenToFirstAction, Attaches, Prompts})`
+ `AttachesPerPrompt()`.
**store**: `Events.UIRuns`, `Events.CountUIActions(action)`, `Events.CountType(type)` (events_stats.go).
**tmux**: `SwitchClientCommand`, `Client.SwitchClient`, `AttachSessionCommand`, `AttachCmd`, `InteractiveCmd`
(attach.go); `PopupBinding` (binding.go). **testenv**: `TmuxServer.AttachControlClient(t, target) *ControlClient`
(`Session(t)`, `Close()`), a `tmux -C attach-session` client (ctlclient.go).

## Contract for the stub files (rewrite whole; do not edit the shell)

All stubs live in package `tui`, so they use the unexported shell types in `view.go`:

- `shared` (built once, never mutated): `ctx` (pass to every controller call), `ctl`, `keys KeyMap`, `runID`, `cwd`,
  `binding`. `sh.actionCmd(action, taskID) tea.Cmd` records `ui.action` for the run (no-op without one) — call it when
  the action really happened.
- `frame{width, height int; st styles}`; `fieldLine(f, label, value)` renders a detail row in the label column.
- `view` interface: `Update(tea.Msg) (view, tea.Cmd)` (return nil to close), `Body(frame) string`, `Hints() []key.Binding`.
  The top view gets every message first (all keys except ctrl+c, window sizes, `refreshedMsg`, its own results).
- Messages: `refreshedMsg{ov}` (broadcast after every Overview load to the top view and the detail sections),
  `flashMsg{text, isErr}` (+ `flashInfo`, `flashError`), `reloadMsg{}`, `selectTaskMsg{id}` (select + reload + list).
- `lineInput` (input.go): single-line editor (Update/View/Value/SetValue, `limit`). bubbles `textinput` / `textarea`
  import `github.com/atotto/clipboard`, which is **not in go.sum**, so using them changes go.mod/go.sum (see director).

| File | Owner | Final signature | Must |
|---|---|---|---|
| `compose.go` | m2-prompt-queue | `func newComposeView(sh *shared, task controller.TaskView) (view, tea.Cmd)` | ctrl+s (`sh.keys.Send`) sends via a tea.Cmd, esc cancels; on success `sh.actionCmd(controller.UIActionPrompt, id)` + `reloadMsg`, nil view |
| `detail_messages.go` | m2-prompt-queue | `type messagesSection`; `newMessagesSection(sh, task) (messagesSection, tea.Cmd)`; `Update(tea.Msg) (messagesSection, tea.Cmd)`; `View(frame) string`; `Hints() []key.Binding` | live delivery states from `refreshedMsg`; `sh.keys.CancelMessage` cancels a queued one (the detail forwards keys it does not use) |
| `diff_view.go` | m2-tui-manage | `func newDiffView(sh *shared, task controller.TaskView) (view, tea.Cmd)` | file list; enter → pager via `tea.ExecProcess`; `UIActionDiff` |
| `logs_view.go` | m2-tui-manage | `func newLogsView(sh *shared, task controller.TaskView) (view, tea.Cmd)` | recent capture; `sh.keys.FullCapture`; `UIActionLogs` |
| `newtask_view.go` | m2-tui-manage | `func newNewTaskView(sh *shared) (view, tea.Cmd)` | form with `lineInput`, NewTask (Cwd `sh.cwd`) async, errors inline; success → `UIActionNew` + `selectTaskMsg{id}` |
| `adopt_view.go` | m2-tui-manage | `func newAdoptView(sh *shared) (view, tea.Cmd)` | `ListAdoptCandidates`/`Adopt`; success → `UIActionAdopt` + `selectTaskMsg{id}` |
| `confirm_view.go` | m2-tui-manage | `func newCloseConfirmView(sh *shared, task controller.TaskView) (view, tea.Cmd)` | `close <task>? y/N` (`sh.keys.Confirm`), stop needs a second confirmation; `PlanClose`/`CloseTask`; `UIActionClose` + `reloadMsg` |
| `detail_inspect.go` | m2-tui-manage | `type inspectSection`; `newInspectSection(sh, task) (inspectSection, tea.Cmd)`; `Update`; `View(frame) string` | diff summary + last ~10 lines, loaded by the returned cmd ("loading…" until then) |

Stubs return `(nil, comingIn(feature, issue))`, which flashes `"<feature>: coming in <issue>"` and records nothing.

**Keymap** (keys.go): action names `up down open back quit force_quit filter prompt diff logs new adopt close
cancel_message send full_capture confirm` (`Actions()`); `DefaultKeyMap()`; `KeyMap.Binding(action)`;
`KeyMap.WithKeys(action, keys...) (KeyMap, error)` returns a copy. m2-tui-manage's `keys_config.go` builds a KeyMap from
config.yaml and its one line in `cmd_tui.go` (marked by a comment) sets `opts.Keys` and `opts.Warning` (banner).

## stats definitions

Runs = `ui.opened` events with a `ui_run_id`; first action = earliest later `ui.action` with that id; median over runs
with an action (mean of the middle two for an even count). Attaches = `ui.action{attach}`; **prompts = `message.queued`
events** (every prompt sent through agentctl, TUI or `agentctl send`). Text and `--json` (`schema_version` 1; nulls when
there is no data). The TUI records `ui.action` only when an action happens: refused attaches and stub keys record nothing.

## Findings

1. **Bubble Tea v2.1.1 data race on the kill path** (`tea.WithContext` cancel or `Program.Kill`): `shutdown(kill=true)`
   closes the input cancelreader without `waitForReadLoop()`, while the reader goroutine is in `os.File.Fd()`. Seen as
   exit status 66 under `-race` on SIGHUP (≈1 in 8). Fix here: `Run` turns ctx cancellation into `p.Quit()` (graceful
   path waits for the read loop); 10/10 clean afterwards. Later cards: never pass `tea.WithContext` / call `Kill`.
2. `colorprofile.Detect` runs `tmux info` (unqualified socket, process env) when the output is a tty and `TMUX` is set.
   `Run` hides TMUX/TMUX_PANE from Bubble Tea's environment so that never happens.
3. `switch-client -t %N` with no `-c` from an unattached CLI client moves the most recently active client (verified with
   a control-mode client on tmux 3.5a) and selects the pane's window and pane; `attach-session -t %N` works too (exec
   path verified in a second isolated server's pane). Inside a real `display-popup` (no `$TMUX_PANE`) tmux resolves
   the client the same way; **m3 demo: confirm the popup lands on the agent pane** (ARCHITECTURE open question).
4. A non-tty output gives Bubble Tea a 0x0 size, so nothing renders: TUI end-to-end tests run the test binary
   (`TestHelperAgentctlTUI`, helper-process pattern, no TestMain) inside isolated tmux panes.
5. bubbles v2 `textinput`/`textarea`/`list` need modules missing from go.sum (`atotto/clipboard`, `sahilm/fuzzy`);
   `key`, `help`, `viewport`, `table` are usable as is.

## Deviations

- Empty state renders the keys bold instead of the doc's backticks: `No agents yet — n new · a adopt a running claude
  pane`, then `Open this from anywhere in tmux; add to tmux.conf:` and the binding (wrapped to the width).
- Attach to a dead-but-verified pane (remain-on-exit; stopped/failed) is allowed so the final output can be read.
  Stored `disconnected` and identity-less sessions are refused with no tmux call; mismatches after the read-only probe.
- esc on the bare list (no filter) quits, per "q/esc back or quit"; q in detail goes back rather than quitting.
- The TUI always loads the Overview with closed tasks and filters client-side.
- Test helpers in `internal/controller/attach_test.go` are prefixed `at` (m1-inspect's test file also has `seedSession`).

## Notes for the director

- Merged main twice (m1-pane-identity + m1-inspect, then m1-hook-state + 2801c2b). After 2801c2b the every-state
  fixture gained a launched agent silent since launch (NEEDS YOU); **goldens regenerated** (`-update`), all under
  `internal/tui/testdata/TestGolden/`.
- If m2-prompt-queue / m2-tui-manage want bubbles `textarea`/`textinput`, adding `github.com/atotto/clipboard` to
  go.mod/go.sum is a director decision; `lineInput` exists to avoid it.
