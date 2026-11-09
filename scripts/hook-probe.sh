#!/usr/bin/env bash
# hook-probe.sh — capture the hook payloads a real Claude Code sends, so the
# hook->state table (internal/adapter/claude/hook.go) and its fixtures
# (internal/adapter/claude/testdata/hooks/*.json) can be checked against
# reality. MANUAL: run it by hand, inside tmux; it is never part of go test.
#
#   scripts/hook-probe.sh [OUT_DIR]          start claude with a logging --settings file
#   scripts/hook-probe.sh --split OUT_DIR    split OUT_DIR/payloads.jsonl into OUT_DIR/fixtures/*.json
#
# CLAUDE_BIN overrides the agent binary (default: claude). The session runs in
# a throwaway git repo under OUT_DIR, so nothing touches your projects. It does
# NOT touch ~/.claude: --settings adds the probe hooks for this session only
# (your own user/project hooks still run too, which is one thing to observe).
#
# Every hook event appends its stdin JSON to OUT_DIR/payloads.jsonl and one
# line to OUT_DIR/env.log with the inherited TMUX / TMUX_PANE /
# CLAUDE_PROJECT_DIR / AGENTCTL_* values. The hook prints nothing on stdout.
#
# Payloads contain your prompts and transcript paths: use throwaway prompts,
# and review the split fixtures before committing any of them.
set -euo pipefail

events=(SessionStart UserPromptSubmit PreToolUse PostToolUse PostToolUseFailure PreCompact PostCompact
	Notification PermissionRequest PermissionDenied Stop StopFailure SubagentStart SubagentStop SessionEnd
	Elicitation ElicitationResult)

split() {
	local out=$1
	[[ -f "${out}/payloads.jsonl" ]] || { echo "hook-probe: no ${out}/payloads.jsonl" >&2; exit 1; }
	mkdir -p "${out}/fixtures"
	python3 -I - "${out}" <<'PY'
import collections, json, os, sys
out = sys.argv[1]
seen = collections.Counter()
with open(os.path.join(out, "payloads.jsonl")) as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            p = json.loads(line)
        except ValueError:
            print("skipping a non-JSON line", file=sys.stderr)
            continue
        ev = p.get("hook_event_name", "unknown")
        disc = p.get("notification_type") or p.get("source") or p.get("reason") or p.get("trigger") or p.get("tool_name") or ""
        name = (ev + ("_" + disc if disc else "")).replace("/", "_")
        seen[name] += 1
        path = os.path.join(out, "fixtures", "%s_%d.json" % (name, seen[name]))
        with open(path, "w") as g:
            json.dump(p, g, indent=2, sort_keys=True)
            g.write("\n")
for name, n in sorted(seen.items()):
    print("%-48s %d" % (name, n))
PY
	echo "hook-probe: fixtures in ${out}/fixtures (review for private text before committing)"
}

if [[ "${1:-}" == "--split" ]]; then
	[[ $# -eq 2 ]] || { echo "usage: $0 --split OUT_DIR" >&2; exit 2; }
	split "$2"
	exit 0
fi

claude_bin=${CLAUDE_BIN:-claude}
command -v "${claude_bin}" >/dev/null || { echo "hook-probe: ${claude_bin} not on PATH" >&2; exit 1; }
[[ -n "${TMUX:-}" ]] || echo "hook-probe: warning: not inside tmux; TMUX/TMUX_PANE will be empty in env.log" >&2

out=${1:-$(mktemp -d "${TMPDIR:-/tmp}/agentctl-hook-probe.XXXXXX")}
mkdir -p "${out}"
out=$(cd "${out}" && pwd -P)
logger="${out}/log-hook.sh"
cat >"${logger}" <<EOF
#!/bin/sh
# Appends this hook's stdin payload and inherited env; prints nothing.
payload=\$(cat)
printf '%s\n' "\$payload" >> '${out}/payloads.jsonl'
printf 'TMUX=%s TMUX_PANE=%s CLAUDE_PROJECT_DIR=%s AGENTCTL_TASK_ID=%s AGENTCTL_HOME=%s\n' \\
	"\${TMUX:-}" "\${TMUX_PANE:-}" "\${CLAUDE_PROJECT_DIR:-}" "\${AGENTCTL_TASK_ID:-}" "\${AGENTCTL_HOME:-}" >> '${out}/env.log'
exit 0
EOF
chmod 0700 "${logger}"

settings="${out}/probe-settings.json"
{
	printf '{\n  "hooks": {\n'
	for i in "${!events[@]}"; do
		sep=","
		((i == ${#events[@]} - 1)) && sep=""
		printf '    "%s": [{"hooks": [{"type": "command", "command": "%s", "timeout": 5}]}]%s\n' "${events[$i]}" "${logger}" "${sep}"
	done
	printf '  }\n}\n'
} >"${settings}"

repo="${out}/repo"
if [[ ! -d "${repo}/.git" ]]; then
	mkdir -p "${repo}"
	git -C "${repo}" init -q -b main
	printf '# hook probe\n' >"${repo}/README.md"
	git -C "${repo}" add README.md
	git -C "${repo}" -c user.name=probe -c user.email=probe@agentctl.invalid commit -q -m init
fi

cat >&2 <<EOF
hook-probe: logging to ${out}/payloads.jsonl
In the session, please exercise (throwaway text only):
  1. a prompt that runs a Bash command needing permission (e.g. "run: touch x"); approve it
  2. a prompt that makes Claude ask you a question (AskUserQuestion); answer it
  3. paste a large multi-line text and submit it (does UserPromptSubmit.prompt hold it, or "[Pasted text #N]"?)
  4. leave it idle for 60+ s (Notification idle_prompt)
  5. /compact, then /clear (SessionStart compact / clear, SessionEnd clear)
  6. exit with ctrl-d twice (SessionEnd)
Then run: $0 --split ${out}
EOF
cd "${repo}"
"${claude_bin}" --settings "${settings}" || true
echo "hook-probe: $(wc -l <"${out}/payloads.jsonl" 2>/dev/null || echo 0) payloads in ${out}/payloads.jsonl" >&2
