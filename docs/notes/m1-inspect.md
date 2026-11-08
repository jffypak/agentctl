# m1-inspect — notes, APIs for later cards, findings

`agentctl show <name> [--full]` and `agentctl diff <name> [--stat]`, backed by
`controller.Inspect` / `DiffSummary` / `WriteFullDiff` / `FullDiffCmd` /
`CaptureFull` / `PagerCmd`. Everything is read-only against git and the pane.

## Files (all owned by this card)

| File | What |
|---|---|
| `internal/gitx/diff.go` | `FileChange`, `Summary`, `Totals`, arg builders, `DiffSummary`, `WriteFullDiff`, `ParseNumstatZ`, `countLines`, `ErrOutputClosed` |
| `internal/gitx/status.go` | `StatusArgs`, `Status`/`StatusEntry`, `ParseStatusZ`, `Conflicts()`, `Untracked()` |
| `internal/tmux/capture.go` | `CaptureTarget`, `CaptureCommands`, `Client.CapturePane`, `CaptureAll`, `ErrPaneMismatch`, `ErrPaneNotFound` |
| `internal/pager/pager.go` | `Resolve`, `Command`, `IsTerminal`, `EnvPager`, `DefaultPager` |
| `internal/controller/inspect.go` | use case + `DiffSummary.Lines` rendering + `DisplayPath` |
| `cmd/agentctl/cmd_show.go`, `cmd_diff.go` | subcommands (stubs rewritten whole; their `registerStub` lines are gone) |
| tests | `gitx/diff_test.go`, `gitx/diff_integration_test.go`, `tmux/capture_test.go`, `tmux/capture_integration_test.go`, `pager/pager_test.go`, `controller/inspect_test.go`, `cmd/agentctl/cmd_show_test.go`, `cmd/agentctl/cmd_diff_test.go` |

## Controller API for m2-tui-manage (TUI detail / diff / logs views)

```go
const DefaultInspectLines = 40
var ErrPaneUnavailable, ErrDiffUnavailable error; var ErrOutputClosed = gitx.ErrOutputClosed

func (c *Controller) Inspect(ctx, taskID string, InspectOptions{Lines int}) (Inspection, error)
//   Inspection{Task TaskView; Pane PaneStatus{Available bool; Liveness domain.Liveness; Note string};
//              Output []string /* nil when unavailable */; Diff DiffSummary; DiffErr string}
func (c *Controller) DiffSummary(ctx, taskID string) (DiffSummary, error)
//   DiffSummary{Base, BaseRef, Head string; Commits int; Committed, Staged, Unstaged, Untracked, Total []FileChange; Conflicts []string}
//   (s DiffSummary) Lines(maxFiles int) []string   // header + 5 sections, "none" when empty; maxFiles<=0 = all
//   (s DiffSummary) Clean() bool;  DisplayPath(p string) string   // Go-quotes non-printable paths
func (c *Controller) WriteFullDiff(ctx, taskID string, w io.Writer, color bool) error
func (c *Controller) FullDiffCmd(ctx, taskID string) (*exec.Cmd, error)   // for tea.ExecProcess
func (c *Controller) CaptureFull(ctx, taskID string) (logPath string, err error)
func (c *Controller) PagerCmd(path string) (*exec.Cmd, error)             // page CaptureFull's log
```

TUI usage: `cmd, err := ctl.FullDiffCmd(ctx, id); tea.ExecProcess(cmd, cb)` — only `Stdin` is
preset (an unlinked temp file holding the colored diff), so ExecProcess attaches the terminal to
Stdout/Stderr; close `cmd.Stdin` in the callback (it is also closed on GC). For logs:
`path, err := ctl.CaptureFull(ctx, id); cmd, _ := ctl.PagerCmd(path)`. `Inspect` never writes the
registry; the TUI should call `Reconcile` on its own cadence (as `show` does before `Inspect`).
`Inspection.Pane.Note` is the exact display string (`alive`, `dead (exit 3)`,
`pane unavailable (disconnected: identity_mismatch)`, `pane unavailable (no session)`, ...).

## Behaviour

- **Identity before capture, twice.** `liveSession` first (classification and reason). Then
  `tmux.CapturePane` sends one `if-shell -F -t %N '#{&&:#{==:#{pid},S},#{==:#{pane_pid},P}}'
  'display-message -p agentctl-capture-ok ; capture-pane -p -J -t %N -S <start> -E -' '<else>'`
  invocation, so the *server* re-checks the identity atomically and a reused `%N` is never
  captured at all (not "captured then discarded"). The else branch prints
  `#{?pane_id,agentctl-capture-mismatch,agentctl-capture-missing}` → `ErrPaneMismatch` /
  `ErrPaneNotFound`. Command strings contain only a validated `%N` and integers.
- A session whose stored state is `disconnected` stays unavailable even if a probe would verify
  (sticky); the reason comes from `last_detail`. Dead panes (`remain-on-exit`) are captured.
- Recent output: capture screen + 2000 history lines, `SanitizeTerminalText`, right-trim, drop
  blank lines, keep the last N (40). Full capture: whole history, sanitized, trailing blank lines
  dropped, written to `logs/<task>/<UTC 20060102T150405.000Z>.log` with `O_EXCL` (a same-ms
  capture gets `-2`, ...), file 0600, dir 0700 (chmod'ed even if it pre-existed). `show --full`
  prints only the path (scriptable: `less "$(agentctl show t --full)"`); unavailable pane → exit 1.
- Diff summary sections: committed (`diff-tree -r --numstat -z -M <base> HEAD` + `rev-list --count`),
  staged (`diff-index --cached … HEAD`), unstaged (`diff-files`), untracked (`status --porcelain=v2
  -z --untracked-files=all`, lines counted in Go: newline count + unterminated last line, NUL in
  first 8000 bytes = binary, symlink = 1, nested repo `dir/` = no count), conflicts (status `u`
  records; removed from staged/unstaged, which list each unmerged stage), and Total
  (`diff-index … <base>`, the net tracked change used in the header).
- Full diff: `diff-index -p -M --color=… <base> --` then, per untracked file,
  `diff --no-index --color=… -- /dev/null <path>` (exit 1 = differences = success). Plain
  (`--color=never`) when stdout is not a tty; colored into the pager on a tty. A reader that goes
  away (SIGPIPE/EPIPE) is `ErrOutputClosed` → exit 0.
- Pager: `$AGENTCTL_PAGER` → `$PAGER` → `less -R`, blank values fall through, always run as
  `/bin/sh -c <line>` with the controller env (like git), so user pagers may carry args. `LESS` is
  not set (git's `-F` would make a one-screen diff vanish instantly under `tea.ExecProcess`).
  A failing pager → `agentctl: pager "<line>" failed: exit status N`, exit 1.

## Findings

1. **Porcelain `git diff` rewrites `.git/index` even with `GIT_OPTIONAL_LOCKS=0`** (git 2.49,
   `refresh_index_quietly` after stat-dirty entries). So every read here is plumbing
   (`diff-tree`/`diff-index`/`diff-files`, which compare content and never write) or `status`
   (which does honour `GIT_OPTIONAL_LOCKS`). `TestDiffSummaryIntegration` includes a negative
   control proving porcelain diff *does* rewrite the index in the same fixture. Plumbing
   `--numstat`/`-p` also skip stat-dirty-but-unchanged files, so no false positives.
   **Later cards: never use porcelain `git diff`/`git diff <rev>` against a worktree.**
2. `if-shell -t` and `display-message -t` tolerate a missing target (formats expand empty, exit 0);
   `capture-pane -t` does not. Hence the explicit missing/mismatch marker.
3. Colored git output splits the `+`/`-` sign from the text (`\e[32m+\e[m\e[32mtext`), so tests
   match text without the sign.
4. `domain.SanitizeTerminalText` keeps bidi controls (U+202A–U+202E, U+2066–U+2069). Pane output
   is printed as sanitized; file paths in the summary are Go-quoted by `DisplayPath`.
   Suggestion for the domain owner: strip or escape bidi overrides too.

## Deviations from ARCHITECTURE.md / the card

1. Signatures take a context and return errors: `FullDiffCmd(ctx, task) (*exec.Cmd, error)`,
   `CaptureFull(ctx, task) (string, error)` (card: `FullDiffCmd(task) *exec.Cmd`,
   `CaptureFull(task) (logPath)`). Added `DiffSummary`, `WriteFullDiff`, `PagerCmd`, `DisplayPath`.
2. `cmd/agentctl` imports `internal/pager` (for `IsTerminal`/`Resolve`) besides controller/domain.
   The TUI may use `controller.FullDiffCmd`/`PagerCmd` instead and never import it.
3. `gitx.stream` repeats `run`'s exec setup (env, `-C`, absolute-dir check) because `gitx.go` is
   skeleton-owned; it streams stdout and accepts extra exit codes. Merge them if gitx.go is reopened.
4. `show` calls `Reconcile` before `Inspect` (same pattern and warning as `ls`).
5. New error kinds `ErrPaneUnavailable`, `ErrDiffUnavailable` live in `inspect.go`, not `errors.go`.
6. No new modules (`golang.org/x/term` was already a direct requirement) and no migrations.

## Acceptance → tests

| Bullet | Tests |
|---|---|
| -z numstat: renames, binaries, spaces/newlines | `gitx.TestParseNumstatZ`, `gitx.TestParseStatusZ`, `gitx_test.TestDiffSummaryIntegration` |
| Summary: committed, staged, unstaged, untracked (+lines), conflicts | `gitx_test.TestDiffSummaryIntegration`, `controller.TestInspectLiveTaskOutputAndDiff`, `controller.TestDiffSummaryLines`, `main.TestDiffStatPrintsSummary` |
| diff: pager on tty, plain otherwise, --no-index exit 1 ok, --stat | `main.TestDiffPipesToPagerOnATerminal`, `main.TestDiffWritesPlainDiffWhenNotATerminal`, `gitx_test.TestWriteFullDiffIncludesUntrackedAsNewFiles`, `controller.TestFullDiffCmdPagesTrackedAndUntracked`, `main.TestDiffStatPrintsSummary` |
| show block + last 40; OSC 52/CSI never in output | `main.TestShowPrintsStateBlockAndLast40Lines`, `controller.TestInspectSanitizesCapturedText`, `tmux_test.TestCapturePaneIntegration` |
| show --full: logs/<task>/<UTC>.log 0600/0700, path printed | `main.TestShowFullWritesPrivateLogAndPrintsPath`, `controller.TestCaptureFullWritesPrivateUTCLog` |
| Disconnected/reused: diff + note, never captured; dead pane captures | `main.TestShowReusedPaneShowsDiffAndNeverCaptures`, `controller.TestInspectReusedPaneShowsDiffButNeverCaptures`, `controller.TestInspectStickyDisconnectedIsNeverCaptured`, `controller.TestInspectServerGone`, `tmux_test.TestCapturePaneReusedPaneIDIsNeverCaptured`, `main.TestShowDeadPaneStillShowsFinalOutput`, `controller.TestInspectDeadPaneStillCapturesFinalOutput` |
| Read-only: index hash/mtime, HEAD, worktree unchanged; no add -N; GIT_OPTIONAL_LOCKS=0 | `main.TestShowAndDiffAreReadOnlyAndUseOptionalLocks0` (git spy on PATH logs every call), `gitx_test.TestDiffSummaryIntegration` (+ negative control), `gitx.TestDiffArgBuildersNeverUsePorcelainWorktreeDiff` |
