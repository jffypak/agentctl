#!/usr/bin/env bash
# doctor.sh is the per-worktree health gate tinkerbell's acceptance pass runs.
# It is exactly scripts/check.sh.
exec "$(dirname "$0")/check.sh" "$@"
