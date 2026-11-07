#!/usr/bin/env bash
# agentctl gate (ARCHITECTURE.md §Testing). Fails if any of these fail:
#   1. gofmt -l is non-empty
#   2. go vet ./...
#   3. go test -race ./...
#   4. go mod tidy would change go.mod or go.sum
# Every step runs; a summary lists the failures. Exit 0 only when all pass.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

failed=()

step() {
	local name=$1
	shift
	echo "==> ${name}"
	if "$@"; then
		echo "ok: ${name}"
	else
		echo "FAIL: ${name}"
		failed+=("${name}")
	fi
}

gofmt_clean() {
	local out
	# Every .go file except those under dot-directories (.git, .tinkerbell worktrees).
	out=$(find . -mindepth 1 -type d -name '.*' -prune -o -type f -name '*.go' -print0 | xargs -0 gofmt -l) || return 1
	if [[ -n "${out}" ]]; then
		echo "gofmt -l reports unformatted files:"
		echo "${out}"
		return 1
	fi
}

tidy_clean() {
	# -diff prints the change go mod tidy would make and exits non-zero; it never writes.
	go mod tidy -diff
}

step "gofmt" gofmt_clean
step "go vet" go vet ./...
step "go test -race" go test -race ./...
step "go mod tidy" tidy_clean

if ((${#failed[@]})); then
	echo "check.sh: FAILED: ${failed[*]}"
	exit 1
fi
echo "check.sh: all checks passed"
