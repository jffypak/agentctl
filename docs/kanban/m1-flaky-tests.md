---
id: m1-flaky-tests
title: Root-cause and fix two load-sensitive flaky tests (SQLite BUSY in store, tmux
  restart in kill_test)
status: done
priority: high
milestone: m1-see-agents
labels:
- spec-derived
- phase-1
- reliability
created: '2026-11-08'
updated: '2026-11-09'
acceptance_criteria: '- Root cause of each flake identified and written up in docs/notes/m1-flaky-tests.md,
  with the evidence (extended error codes, reproduction)

  - A deterministic or load-reproducing test demonstrates each root cause before the
  fix

  - Original bars unchanged: TestConcurrentWritersNoBusy still runs 8 goroutines +
  2 OS processes (hook busy timeout) with zero SQLITE_BUSY; the kill test still proves
  a reused %N is never killed

  - Under load (full `go test -race -count=1 ./...` run 10 times back-to-back while
  CPU burners occupy most cores), both tests pass 10/10

  - No store API signature changes; no new modules or migrations'
history:
- when: '2026-11-08'
  from: null
  to: todo
- when: '2026-11-08'
  from: todo
  to: in-progress
  note: 'director: dispatched to worktree /Users/jpark/Code/agent-tui/.tinkerbell/worktrees/m1-flaky-tests
    (tb/m1-flaky-tests)'
- when: '2026-11-09'
  from: in-progress
  to: done
  note: "director: done \u2014 acceptance pass green; promote to done"
---

# Root-cause and fix two load-sensitive flaky tests (SQLite BUSY in store, tmux restart in kill_test)

> **Read `docs/ARCHITECTURE.md` in full before starting** (dispatch prompts clip it at 4000 chars) and obey §Code conventions & file ownership: edit only owned files, findings in `docs/notes/m1-flaky-tests.md`, commit at checkpoints, finish with `scripts/doctor.sh` green.


**Why:** the merge gate (`go test -race ./...`) is flaky under realistic machine load
(load avg ~12 on 16 cores while sibling agents run suites). A flaky gate blocks every
later merge, and one flake may be a real product bug: a hook process losing a state
write means stale agent state, which is exactly what invariant 3 (no blind
injection) depends on.

**Flake 1: `internal/store` `TestConcurrentWritersNoBusy`.** Fails about 1 in 3
full-suite runs under load: an OS-process writer (opened with `HookBusyTimeout`
1.5s) gets `database is locked (5) (SQLITE_BUSY)` from `Events.Append`
(autocommit, not `withTx`), sometimes on its very first append. It passes when the
store package runs alone. What the director already ruled out:
- The busy timeout is applied (`PRAGMA busy_timeout`=5000 / 1500).
- The modernc busy handler is fine-grained (a writer waits about the lock-hold
  time plus under 40ms).
- Pacing the writers at 20ms between statements did **not** fix it, so plain
  lock contention is an unlikely explanation.

Suspects to test, not assume: WAL read-snapshot then write upgrade (`SQLITE_BUSY`
returned without invoking the busy handler); the database/sql pool keeping a read
transaction open; checkpoint or WAL-restart interaction; the migrator running
inside helper-process `Open` while writers are active; a lock held across a
descheduled goroutine.

**Flake 2: `internal/tmux` `TestKillVerifiedSessionNeverHitsAReusedPaneAfterRestart`.**
Failed once in 5 loaded full-suite runs. Root-cause it the same way (timing
assumption around the isolated server restart?).

**Method:** use systematic debugging. Reproduce under load first (for example,
run the full suite while `stress`-style CPU burners run, or with
`-count=N -cpu`), capture the extended error code, prove the root cause with a
targeted test, then fix the cause. Do **not** weaken a bar (no longer timeouts in
tests, no skips, no retry-the-test). If the real fix is product-side, for example
routing `Events.Append` and task reservation through `withTx` (BEGIN IMMEDIATE)
or retrying `SQLITE_BUSY_SNAPSHOT` inside the hook budget, make it there, keeping
store API signatures unchanged.

**Owns:** `internal/store/{store.go, events.go, tasks.go, concurrency_test.go}`,
new `internal/store/busy_*_test.go`, `internal/tmux/{kill.go, kill_test.go}`,
`docs/notes/m1-flaky-tests.md`. Do not touch `sessions_hook.go` (m1-hook-state,
in flight) or `events_stats.go` (m2-tui-inbox, in flight).
