# m2-hardening-followups — notes

This card covers the three merge follow-ups from `docs/notes/m1-flaky-tests.md`.
No API signatures changed, and there are no new modules or migrations.

## 1. testenv `Kill` / `Restart` wait for the old server pid (`internal/testenv/tmux.go`)

- `Kill` first takes one snapshot (server pid plus pane pids), then sends
  `kill-server`. It returns only once the old server pid reports `ESRCH`. The
  wait polls `syscall.Kill(pid, 0)` and is bounded by `tmuxCallTimeout` (10 s).
  It is a condition wait, not a sleep.
- `Restart` is `Kill` then start, so it can no longer reach a dying server.
- A server that no longer answers may already be dying, so the pid falls back
  to the one `start` recorded. Tests have no way to stop a testenv server
  other than through testenv (archtest keeps `kill-server` in testenv), so
  that pid can't be stale.
- New helpers, used only in tests:
  - `KillNoWait(t) pid` sends the kill and returns without waiting, for tests
    that observe the dying state.
  - `WaitExit(t, pid)` waits for that pid to exit.
- `kill_test.go`:
  - `restartAfterExit` and `waitServerExit` are gone. The reuse test uses plain
    `srv.Restart(t)`.
  - `TestRestartAfterKillReachesTheDyingServer` now uses `KillNoWait` and
    `WaitExit`, because a plain `Kill` would block while the server is held.

### Knock-on fix: cleanup waits for pane processes

Waiting for the server moved `t.Cleanup`'s temp-dir `RemoveAll` into the
window where a SIGHUPed agent is still writing. fakeagent answers SIGHUP by
running its SessionEnd hook, and that hook writes into `AGENTCTL_HOME`.

- Before this fix, the old helper's `RemoveAll` usually won that race outright.
- With the server wait alone, `cmd/agentctl` `TestAdoptedPaneHooksViaSnippet`
  failed 2 of 8 loaded runs ("directory not empty"). Base af5c8b6 failed 0 of 8.
- The fix: `Kill` records the pane pids, and the server's cleanup waits for
  them to exit. The wait is best effort and bounded at 10 s.
- After the fix: 0 failures in 12 loaded runs.
- `Kill` itself does not wait for panes. A test that asserts right after a
  `Kill` sees the same agent timing as before.

### Tests (`internal/tmux/restart_hardening_test.go`)

The server is held in its dying state by a SIGSTOPped control-mode client
(`holdDyingServer`).

| Test | Against the old helper | Against the new helper |
|---|---|---|
| `TestKillWaitsForTheOldServerToExit` | fails every run: "Kill returned while server N was held in its dying state" | passes |
| `TestRestartWaitsForTheOldServerToExit` | fails about 25% of runs (5 of 25) | passes |
| `TestSnapshotOfADyingServerIsErrNoServer` | fails (see item 2) | passes |

- **Kill test:** runs `Kill` in a goroutine. It must still be blocked once the
  server is observably dying, and it must return only after the hold is
  released and the pid is gone.
- **Restart test:** catches the old helper only part of the time. With the
  old helper, `start-server`'s client blocks on the dying socket until the
  hold is released, then races the exit. `Restart` is just `Kill` plus start,
  so the deterministic proof is the `Kill` test.

## 2. `isNoServer` covers a dying server (`internal/tmux/tmux.go`)

- `"server exited unexpectedly"` (tmux 3.x) and `"lost server"` (older tmux)
  now unwrap to `ErrNoServer`, matched as prefixes like the other two messages.
- `TestIsNoServerCoversADyingServer` is the table test, with near-miss
  negatives.
- `TestSnapshotOfADyingServerIsErrNoServer` checks a real dying server.
- `internal/controller/reconcile_hardening_test.go`
  `TestReconcileAgainstADyingServerIsServerGone` checks the controller side:
  - Setup: real tasks, then a controller whose runner answers like a dying
    server.
  - Result: both sessions become `disconnected{server_gone}` with one
    `session.disconnected` event each, and a second reconcile changes nothing.
  - Before the fix, `Reconcile` returned "cannot probe tmux … server exited
    unexpectedly" and left the sessions unverified.
  - A fake runner is needed here because archtest bans exec of tmux outside
    `internal/tmux`, so a controller test can't hold a real server.
- The same mapping applies to `adapter/claude` `Observe`, which reports
  `server_gone`.

## 3. `Repos.Upsert` through `withTx` (`internal/store/repos.go`)

- The same `INSERT … ON CONFLICT … RETURNING` now runs inside `withTx`, so
  every store write takes the fair write-lock path. `migrate.go` keeps its own
  transaction, as noted in m1.
- `TestRepoUpsertTakesTheWriteLockThroughWithTx` (`repos_hardening_test.go`) is
  white-box. It checks that `Upsert` stamps the release time and opens the
  write pool, which only `withTx` does, and that the upsert behaviour is
  unchanged.
- The existing store tests pass.

## Results under load

Full `go test -race -count=1 ./...`, 5 runs back to back, with 12 CPU burners
(`perl` loops with `alarm` bounds, killed afterwards; load average 16–30):
**5/5 green** both before and after the cleanup fix (final code: load average 23–39). `scripts/doctor.sh` is green.

## For the director at merge

- Nothing touches m2-prompt-queue's files.
- `hookingest/busy.go` `retryFastBusy` can still be deleted later (m1 note 4).
  It isn't in this card's files.
