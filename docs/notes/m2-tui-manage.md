# m2-tui-manage — notes, contracts, findings

The TUI can now inspect and manage agents without leaving the popup: detail diff + output, `d` diff, `l` logs, `n` new
task, `a` adopt, `x` close (and stop), plus `config.yaml` key bindings. Gate: `scripts/doctor.sh`. No new modules, no
migrations (`bubbles/v2/textinput` stays pinned but unused, see deviations).

## What exists

| File | What |
|---|---|
| `internal/config/config.go` | `Path(getenv)` (= `paths.Resolve(...).ConfigFile`, "" without HOME/XDG), `Load(path)`, `Parse([]byte)`, `Config{Keys map[string][]string}`, `ErrMalformed`. Missing/empty file = zero Config. Strict: unknown top-level settings, wrong shapes, a second YAML document → `ErrMalformed` (one line). Key values are taken verbatim (`y` is "y", not a YAML 1.1 bool). |
| `internal/tui/keys_config.go` | `ConfigKeyMap(getenv, warning) (*KeyMap, string)` (cmd_tui's one line), `KeyMapFromConfig(map) (KeyMap, error)`. Applied whole or not at all: unknown action, empty/blank key, a key bound to two actions (globally unique), or `confirm` on `enter`/`esc` → defaults + one banner line naming every problem. |
| `internal/tui/detail_inspect.go` | `inspectSection`: `Inspect(Lines: 10)` in a tea.Cmd; `diff`/`output` rows ("loading…" until then); re-inspects when state/since/detail change; retries a lost answer (see findings). Helpers `diffHeadline`, `displayLine`, `labeled`, `plural`. |
| `internal/tui/diff_view.go` | `diffView` + the helpers every manage view uses: `nextViewID`, `scroll`, `viewTitle`, `pairHint`, `textHint`, `moreLine`, `selectTask`, `closeStdin`, `viewChrome`. |
| `internal/tui/logs_view.go`, `newtask_view.go`, `adopt_view.go`, `confirm_view.go` | the views (`newtask_view.go` also has `errorLines`, `safeLine`, `flashErr`, `formField`, `formHint`, `slugHint`). |
| tests | `manage_test.go`, `manage_fixture_test.go` (`fakeTmux`: snapshot, has-session, new-session, guarded capture, guarded stop), `manage_golden_test.go` (+ `testdata/TestManageGolden/`), `keys_config_test.go`, `internal/config/config_test.go`, `cmd/agentctl/cmd_tui_keys_test.go` (real terminal). |

Every view is a value; each instance has an id and its async results carry it, so an answer for a closed view never
lands in a newer one. Every controller call runs in a tea.Cmd (`TestManageKeysNeverBlockUpdate`: tmux at 300ms per
call, every key's Update < 50ms). Everything printed from outside (paths, tmux names, errors, config snippets) goes
through `safeLine`/`DisplayPath`/`displayLine` (`TestManageViewsNeverPrintRawEscapes`).

## Behaviour

- **Detail:** `diff  2 files +4 -1 · 1 untracked · 1 commit`, `output  alive` then the newest lines that fit (≤10;
  ~5 at 80x24), or `output  pane unavailable (disconnected: identity_mismatch)` (never captured).
- **d:** title, `vs main @ <sha8> · HEAD <sha8>`, headline, then the net change: tracked files (renamed / binary /
  conflict marked), untracked files, conflicted paths without a tracked change. ↑↓ (+pgup/pgdown/home/end) scroll;
  enter → `FullDiffCmd` → `tea.ExecProcess` → back to the same view, reloaded; closes the pager's stdin. Git only, so
  disconnected tasks work; a missing worktree is a wrapped `! …` line and enter does nothing.
- **l:** `Inspect(Lines: 2000)` snapshot, newest at the bottom, scrollable; `f` → `CaptureFull` + `PagerCmd` →
  ExecProcess → back with `full capture saved to <path>`. Unverifiable pane: `pane unavailable (…)`, `f` explains.
- **n:** `name`/`goal` lineInputs; the slug is validated as typed; enter on the name moves to the goal, enter on the
  goal submits (`NewTask{Cwd: sh.cwd}`); keys are ignored while it runs ("creating x…"). Refusals stay inline with
  both fields kept and focus on the field to fix. Success → `ui.action{new}` + `selectTaskMsg` (row selected,
  starting) + a flash with branch and worktree. Created-but-launch-failed → same, with the error flashed.
- **a:** candidates for `AGENTCTL_ADOPT_COMMANDS` (default claude); `a` toggles every unregistered pane; enter → name
  step (suggested from the pane's directory) → `Adopt` → row selected in UNKNOWN + `ui.action{adopt}`.
- **x:** `close <task>? y/N` — only `confirm` (y) is yes; every other key cancels and changes nothing. `x` again →
  `stop and close <task>? y/N`, offered only when `PlanClose(Stop: true)` succeeded (launched + identity verified now);
  a launched-but-unverifiable task shows `close and stop unavailable: <reason>`. On a closed task: flash
  "x is already closed". Success → `ui.action{close}` + `selectTaskMsg` (back to the list) + flash.

## Acceptance → tests

| Bullet | Tests |
|---|---|
| Detail diff + last ~10 lines async, 'loading…', Update < 50ms | `TestDetailInspectLoadsAsync`, `TestDetailInspectFollowsStateChanges`, `TestDetailInspectRetriesALostResult`, `TestManageKeysNeverBlockUpdate`, golden `detail_inspected_*` |
| d file list; enter pager via ExecProcess, back to same view; disconnected | `TestDiffViewListsFilesAndPagesTheFullDiff`, `TestDiffViewWorksForDisconnectedTasksAndShowsErrors` |
| l scrollable; f full capture paged; mismatched pane 'pane unavailable' | `TestLogsViewScrollsAndPagesTheFullCapture`, `TestLogsViewMismatchedPaneIsUnavailable` |
| n form, slug validation, async, inline errors (branch-exists / not-a-repo / agent-missing), new row selected starting | `TestNewTaskFormValidatesAndCreates`, `TestNewTaskErrorsKeepTheForm` |
| a picker → pane + name → UNKNOWN | `TestAdoptPickerLandsTheRowInUnknown`, `TestAdoptPickerEmptyAndErrors`, `TestAdoptPickerDropsStaleLists` |
| x y/N default No; close-and-stop launched only, second confirmation | `TestCloseConfirmDefaultsToNo`, `TestCloseAndStopNeedsASecondConfirmation`, `TestCloseAndStopOnlyForVerifiedLaunchedSessions`, `TestCloseOnAClosedTaskSaysSo` |
| config.yaml keys: overrides; unknown/conflict → warning + defaults; missing fine; malformed → banner | `config.TestLoad*`, `TestKeyMapFromConfig*`, `TestConfigKeyMap`, `TestConfigKeysDriveTheTUI`, `main.TestTUIConfigKeys` (real terminal) |
| Goldens both sizes; ui.action for diff/logs/new/adopt/close | `TestManageGolden` (diff, diff_error, logs, logs_unavailable, new_error, new_invalid, adopt, adopt_name, confirm, confirm_stop, detail_inspected × 80x24/120x40); `uiActions` asserted in each behaviour test |

## Deviations

1. **Files outside the list (unavoidable):** `internal/tui/app_test.go` `TestStubKeysSayComingIn` now covers only `p`
   (d/l/n/a/x are real); the four inbox detail goldens (`testdata/TestGolden/detail*`) were regenerated — the only
   change is the two `diff`/`output loading…` rows (press() drops commands). New test files and
   `internal/config/` are new, not someone else's.
2. Close success sends `selectTaskMsg{id}` (select + reload + **list**) instead of a bare `reloadMsg`, so closing from
   the detail returns to the list instead of leaving the user on a closed task.
3. ui.action is recorded when the action produced something: diff once the file list loaded, logs once output was
   shown (not for an unavailable pane — like a refused attach), new on create, adopt/close on success.
4. Fixed, non-configurable keys: pgup/pgdown/home/end (scrolling); in text forms tab/shift+tab/↑/↓ move fields and
   enter/esc always submit/cancel (so a printable rebinding of open/back cannot make a form unusable).
5. Text inputs use `lineInput`, not `bubbles/v2/textinput`: textinput's cursor blinks (the inbox bans animation) and
   lineInput is what the filter uses. textinput stays pinned in `internal/deps/pin.go`.
6. The diff view lists the net change (base → worktree + untracked + conflicts); per-stage detail is
   `agentctl diff --stat`. enter pages the whole diff (a user-configured pager cannot be positioned per file).
7. Logs use `Inspect(Lines: 2000)`, which also computes a diff summary (there is no capture-only controller API).

## Findings

1. **The shell drops async results meant for the detail while a view is open** (`Model.forward` gives every non-refresh
   message only to the top view). `inspectSection` works around it (re-requests on a refresh when its request is older
   than 2s). **m2-prompt-queue's `messagesSection` has the same exposure.** Suggested fix for the tui-inbox owner:
   in `forward`, also hand non-key messages to the detail when a view is on top.
2. The inbox test helper `run`/`collect` drops any command slower than 200ms (meant for ticks); real-git commands
   exceed that under a parallel `-race` run. Manage tests use `runLong`/`collectLong` (20s bound, loop guard).
3. `sectionsOf` (app_test.go) counts the flash line as a row; manage tests use `rowsOf` (flash cleared).

## Notes for the director / other cards

- No migrations, no go.mod/go.sum change. Merge risk: `app_test.go` (one hunk) and `testdata/TestGolden/detail*` —
  if m2-prompt-queue changes the detail too, regenerate after merging both:
  `go test ./internal/tui -run 'TestGolden|TestManageGolden' -update`.
- m2-prompt-queue may reuse `safeLine`, `flashErr`, `errorLines`, `formField`, `nextViewID`, `pairHint`, `textHint`.
- m3-acceptance-e2e: AC3 ("view recent output and diff, and return") is covered at model level here; the real-terminal
  pattern is `cmd/agentctl/cmd_tui_keys_test.go` (D opens the diff, esc returns).
