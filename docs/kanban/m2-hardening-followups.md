---
id: m2-hardening-followups
title: Hardening follow-ups from m1-flaky-tests (testenv restart race, dying-server
  handling, Repos.Upsert via withTx)
status: todo
priority: medium
milestone: m2-direct-agents
labels:
- phase-1
- reliability
dependencies:
- m1-flaky-tests
created: '2026-11-09'
updated: '2026-11-09'
acceptance_criteria: '- A test proves Restart/Kill wait for the old server pid to
  exit (it fails against the old helper, e.g. by using a SIGSTOPped control client
  to hold the dying server)

  - isNoServer maps ''server exited unexpectedly'' and ''lost server'' to ErrNoServer
  (table test); reconcile against a dying server yields disconnected{server_gone}

  - Repos.Upsert goes through withTx; the existing store tests still pass

  - Full go test -race ./... passes 5/5 under load (bounded burners, killed afterwards);
  no API signature changes, no modules or migrations'
history:
- when: '2026-11-09'
  from: null
  to: todo
---

# Hardening follow-ups from m1-flaky-tests (testenv restart race, dying-server handling, Repos.Upsert via withTx)

> **Read `docs/ARCHITECTURE.md` and `docs/notes/m1-flaky-tests.md` in full first.** Edit only owned files, put notes in `docs/notes/m2-hardening-followups.md`, commit at checkpoints, and finish with `scripts/doctor.sh` green.

**Why:** these are three follow-ups from m1-flaky-tests (see its notes, "For the director at merge").

1. `internal/testenv/tmux.go`: `Restart` and `Kill` must wait until the old server pid has exited before returning or restarting. `kill-server`'s client returns while the dying server still owns the socket. The same race exists in every Restart/Kill test (controller, adapter/claude, tmux integration). Once this is fixed, `internal/tmux/kill_test.go` `restartAfterExit` can go back to plain `srv.Restart`.
2. `internal/tmux/tmux.go` `isNoServer`: treat "server exited unexpectedly" and "lost server" as `ErrNoServer`, so a reconcile that reaches a dying server reports `server_gone` instead of an error.
3. `internal/store/repos.go` `Repos.Upsert`: route it through `withTx`, so every store write uses the fair write-lock path.

**Owns:** `internal/testenv/tmux.go`, `internal/tmux/tmux.go`, `internal/tmux/kill_test.go`, `internal/store/repos.go`, new `*_hardening_test.go` files, and `docs/notes/m2-hardening-followups.md`. Don't touch files owned by m2-prompt-queue, which runs in parallel (tmux/buffer.go, store/messages.go, hookingest, controller send/drain/deliver, tui compose).
