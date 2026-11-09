# m1-flaky-tests — notes

Two load-sensitive flakes in the merge gate, each root-caused with evidence and
fixed at the cause. The original bars are unchanged.

## Flake 1: `internal/store` `TestConcurrentWritersNoBusy`

### Root cause: busy-handler starvation (not BUSY without the handler)

A writer waiting for the SQLite write lock re-probed only on SQLite's
busy-handler back-off. The sleeps run 1, 2, 5, 10, 15, 20, 25, 25, 25, 50,
50, 100, 100 … ms, which is about 25 probes in the hook's 1.5 s. A writer
that has just committed takes the lock again straight away, and every
transaction holds the lock for 1–10 ms under `-race` and load. So the
hook-timeout writer could miss every moment the lock was free, and it failed
with plain `SQLITE_BUSY` after its **whole** timeout, even though the lock
changed hands hundreds of times. The helper processes failed and the
goroutines didn't only because the helpers have the shortest timeout
(`HookBusyTimeout`, 1.5 s vs 5 s).

### Evidence

From an instrumented copy of the test (a scratch probe, since deleted). It
timed every write across processes and analysed each failure window:

| Runs | Failing runs | Every failure |
|---|---|---|
| 22 unloaded | 4–12 of 22 runs; 1 helper op per failing run, usually `i=0`–`2` | `Code()` = **5** (plain `SQLITE_BUSY`; extended codes are on in modernc, `conn.go:130`), elapsed **1517–1534 ms** (= the full timeout), **833–892 commits by other writers inside the window**, longest commit-free gap 19–57 ms |
| 16 under 14 burners | 9/16 | same: code 5, 1522–1562 ms, 609–750 commits in the window |

Same result with a deadline context on the hook path (modernc's ctx-aware
busy handler), and nothing else showed up. I never saw a `BUSY_SNAPSHOT`
(517), a `BUSY_RECOVERY` (261), a fast BUSY, a stuck lock holder, or the
migrator writing (helper `Open` took ~5 ms and wrote nothing). The director's
check that "pacing at 20 ms didn't fix it" fits this: ten writers paced at
20 ms still keep the lock busy most of the time, and sparse probes still miss.

**On m1-hook-state's "BEGIN IMMEDIATE returns BUSY at once":** that helper
never printed elapsed time, so "at once" was inferred. I couldn't reproduce a
fast BUSY in ~70 timed runs. `hookingest/busy.go` (`retryFastBusy`) is
harmless but now redundant: `withTx` retries every `SQLITE_BUSY*` code
(including a busy connection open) until the busy deadline. A store BUSY now
always takes ≥ `BusyTimeout`, so `retryFastBusy`'s `< 100 ms` branch never
fires.

### Reproduction test: `TestHookWriterIsNotStarvedByBackToBackWriters` (`busy_starvation_test.go`)

Three writers run transactions back to back, each holding the write lock for
10 ms; two share one Store and one uses a second Store. A hook-timeout Store
makes 8 append+transition pairs, each started cold (after the others have
committed 5 more times). Results:

- **Before the fix: 9/9 runs failed.** Each starved op: code 5 after ~1.53 s,
  with ~120 commits by the others meanwhile.
- **After the fix: 0 failures** in every run since.

### Fix (product side, `store.go`, `events.go`, `tasks.go`; API unchanged, no migrations)

- `withTx` begins on a **write pool opened with `_busy_timeout=0`**: no
  SQLite busy handler, so a held lock fails at once. `beginWrite` then
  retries `BEGIN IMMEDIATE` itself every ~1 ms (jittered 0.5–1.5 ms) until
  the Store's own busy timeout (5 s / 1.5 s). That is ~1000+ probes instead
  of ~25. Reads, migrations and `DB()` keep the original pool and its busy
  timeout.
- **Anti-overtaking yield:** a Store leaves the lock alone for `writeYield`
  (2 ms) after releasing it, so a writer that's already waiting gets the next
  turn.
  - Ablation under 15 burners: without the yield, the reproduction test
    failed 2/6; with it, 0/6.
  - Cost: back-to-back writes from one Store are spaced ≥ 2 ms. Writers that
    pause between writes (hooks, the TUI) pay nothing.
- `Events.Append` and `Tasks.Reserve` now run in `withTx`, so every write the
  store owns takes the lock this way. Event validation happens before the
  lock is taken.

## Flake 2: `internal/tmux` `TestKillVerifiedSessionNeverHitsAReusedPaneAfterRestart`

### Root cause: restart raced the dying server (harness)

`testenv.Restart` is Kill and then start. The kill command's client returns
when *its* exit handshake is done. The old server process lives on, still
owning the socket and dropping new clients, until its event loop sees every
client gone. Under load the follow-up `start-server` connected to that dying
server.

### Evidence

- 1 failure in 12 loaded full-suite runs, failing at `Restart` (kill_test.go:105):
  `start tmux server agentctl-test-847a1867: tmux start-server+set-option: server exited unexpectedly`.
- Hermetic shell experiment, with a stopped control-mode client holding the
  killed server:
  - the kill command exits 0, but the old pid is still alive;
  - `start-server` and `list-panes` exit 1 with "server exited unexpectedly";
  - once the old pid has exited, `start-server` works.

### Reproduction test: `TestRestartAfterKillReachesTheDyingServer` (`kill_test.go`)

- `holdDyingServer` attaches a control-mode client and SIGSTOPs it, so the
  killed server stays in the dying state deterministically.
- The test asserts the server is still alive after the kill and that a
  client connecting then gets "server exited unexpectedly". After release,
  waiting for the pid to exit makes the restart work, with `$N/%N` reused.
- The original restart sequence run under the same hold failed 5/5 with the
  flake's exact message.

### Fix (`kill_test.go`)

The kill test restarts through `restartAfterExit`: PID, then Kill, then
`WaitFor` the old pid to report `ESRCH`, then Restart. It's a condition wait,
not a sleep or a longer timeout. The reused-%N assertion is unchanged.

## Results under load

Full `go test -race -count=1 ./...`, 10 runs back to back, with 14 CPU
burners plus the sibling agents' load (load average 32–61): **suite 10/10 green; `TestConcurrentWritersNoBusy` 0/10 failures; `TestKillVerifiedSessionNeverHitsAReusedPaneAfterRestart` 0/10 failures** (branch merged with main at 463b576).
Earlier loaded runs, store fix in but before the tmux fix: store test 0/12
failures; kill test 1/12 (the restart race above).

## For the director (cross-card, not in my files)

1. **`internal/testenv/tmux.go` (recommended at merge):** `Restart` and
   `Kill` should wait for the old server pid to exit (the same `WaitFor` on
   `syscall.Kill(pid, 0)` → `ESRCH`). Every other Restart/Kill user carries
   the same race:
   - `controller` adopt/inspect/close/controller tests, `adapter/claude`
     stop/observe tests, `tmux` capture/integration tests;
   - `TestSnapshotEmptyKilledAndRestartedServer` in particular expects
     `ErrNoServer` right after Kill. Once testenv does this,
     `restartAfterExit` in `kill_test.go` can become plain `srv.Restart(t)`.
2. **`internal/tmux/tmux.go` `isNoServer` (product, small):** treat
   `server exited unexpectedly` (and `lost server`) as `ErrNoServer`. When a
   user kills their server, a call can reach it while it's dying, and today
   that surfaces as an error for one reconcile tick instead of "server gone".
3. **`internal/store/repos.go` `Repos.Upsert`** is the one remaining
   autocommit write on SQLite's busy handler (rare: `new`/`adopt` only).
   Routing it through `withTx` would make every store write fair.
   `migrate.go` keeps its own `BeginTx` on the read pool (fine: it only
   writes on first open).
4. `hookingest/busy.go` `retryFastBusy` can be deleted later (see above);
   it's compatible as is.
