#!/usr/bin/env bash
# demo.sh — the Phase-1 two-agent demo (ARCHITECTURE.md §Testing, "Real agent").
#
#   DEMO_AGENT=fakeagent scripts/demo.sh    hermetic; exercised by go test (internal/e2e)
#   DEMO_AGENT=claude    scripts/demo.sh    real Claude Code, run once by hand
#
# Everything happens in a throwaway directory: a fresh git repo, a private
# AGENTCTL_HOME, and an isolated tmux server on its own socket
# (tmux -L $DEMO_SOCKET, default agentctl-demo). It never touches your default
# tmux server, your repos or your agentctl registry; cleanup kills only the
# demo socket's server and deletes only the directory it created.
#
# Steps: two agents (alpha, beta) in their own worktrees -> wait for each
# turn -> a multi-line prompt and a second prompt to alpha (deliver-or-queue,
# acknowledgement by hash or timing) -> an edit in alpha's worktree, diff and
# recent output -> the TUI opened in a pane, d (diff), esc, q -> agentctl stats
# (prints the open -> first action median) -> findings -> close alpha with
# --stop, beta without -> worktrees and branches are kept.
#
# Knobs (environment):
#   DEMO_AGENT          fakeagent (default) | claude
#   DEMO_SOCKET         tmux -L socket name (default agentctl-demo); an existing
#                       server on it must have no sessions
#   DEMO_DIR            work directory (default: a new mktemp dir, deleted at exit)
#   DEMO_KEEP=1         keep DEMO_DIR (the registry, logs, worktrees) for inspection
#   DEMO_PAUSE=1        before closing, print the manual checklist and wait for
#                       Enter (default 1 for claude on a terminal, else 0)
#   DEMO_TIMEOUT        seconds to wait for each agent step (fakeagent 30, claude 300)
#   AGENTCTL_BIN        prebuilt agentctl (default: go build into DEMO_DIR/bin)
#   FAKEAGENT_BIN       prebuilt fakeagent (default: go build into DEMO_DIR/bin)
#
# In fakeagent mode every check is strict: the first failure exits 1. In claude
# mode a failed check is printed as a FINDING and the demo goes on, so one run
# answers as many of ARCHITECTURE.md's open questions as possible. The final
# block (FINDINGS) is what the director folds into ARCHITECTURE.md.
set -euo pipefail

DEMO_AGENT=${DEMO_AGENT:-fakeagent}
DEMO_SOCKET=${DEMO_SOCKET:-agentctl-demo}
case "${DEMO_AGENT}" in
fakeagent) default_timeout=30 ;;
claude) default_timeout=300 ;;
*)
	echo "demo: DEMO_AGENT must be fakeagent or claude (got ${DEMO_AGENT})" >&2
	exit 2
	;;
esac
DEMO_TIMEOUT=${DEMO_TIMEOUT:-${default_timeout}}
if [[ -z ${DEMO_PAUSE:-} ]]; then
	DEMO_PAUSE=0
	if [[ ${DEMO_AGENT} == claude && -t 0 ]]; then
		DEMO_PAUSE=1
	fi
fi
if [[ ! ${DEMO_SOCKET} =~ ^[A-Za-z0-9_.-]+$ ]]; then
	echo "demo: DEMO_SOCKET must be a plain -L name (got ${DEMO_SOCKET})" >&2
	exit 2
fi

root=$(cd "$(dirname "$0")/.." && pwd -P)
findings=()
failures=0

log() { printf 'demo: %s\n' "$*"; }
finding() {
	printf 'FINDING: %s\n' "$*"
	findings+=("$*")
}
# check_failed: strict (fakeagent) exits; claude mode records a finding.
check_failed() {
	if [[ ${DEMO_AGENT} == fakeagent ]]; then
		printf 'demo: FAILED: %s\n' "$*" >&2
		exit 1
	fi
	failures=$((failures + 1))
	finding "check failed: $*"
}
need() {
	command -v "$1" >/dev/null 2>&1 || {
		echo "demo: $1 not found on PATH ($2)" >&2
		exit 1
	}
}

# Every tmux call goes through tm: always the demo socket, never the default
# server (invariant 8). The server ignores the user's tmux.conf.
tm() { tmux -L "${DEMO_SOCKET}" -f /dev/null "$@"; }

need tmux "install tmux 3.3 or newer"
need git "install git 2.40 or newer"
if [[ ${DEMO_AGENT} == claude ]]; then
	need claude "install Claude Code and log in first"
fi

if out=$(tm list-sessions -F '#{session_name}' 2>/dev/null) && [[ -n ${out} ]]; then
	echo "demo: the demo server (tmux -L ${DEMO_SOCKET}) already has sessions (${out//$'\n'/ });" >&2
	echo "demo: look with \`tmux -L ${DEMO_SOCKET} ls\`, or stop it with \`tmux -L ${DEMO_SOCKET} kill-server\`" >&2
	exit 1
fi

own_dir=0
socket=""
if [[ -z ${DEMO_DIR:-} ]]; then
	DEMO_DIR=$(mktemp -d "${TMPDIR:-/tmp}/agentctl-demo.XXXXXX")
	own_dir=1
fi
mkdir -p "${DEMO_DIR}"
DEMO_DIR=$(cd "${DEMO_DIR}" && pwd -P)
if [[ ${DEMO_DIR} == *[\"\\]* ]]; then
	echo "demo: DEMO_DIR must not contain quotes or backslashes" >&2
	exit 1
fi

cleanup() {
	local status=$?
	tm kill-server >/dev/null 2>&1 || true # the demo socket's server only
	if [[ -n ${socket:-} && -S ${socket} && ${socket##*/} == "${DEMO_SOCKET}" ]]; then
		rm -f "${socket}" # kill-server leaves the socket file behind
	fi
	if [[ ${own_dir} == 1 && ${DEMO_KEEP:-0} != 1 && ${DEMO_DIR} == */agentctl-demo.* ]]; then
		rm -rf "${DEMO_DIR}"
	elif [[ ${DEMO_KEEP:-0} == 1 ]]; then
		log "kept ${DEMO_DIR}"
	fi
	exit "${status}"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# ---- binaries ---------------------------------------------------------------
mkdir -p "${DEMO_DIR}/bin"
if [[ -z ${AGENTCTL_BIN:-} ]]; then
	need go "or set AGENTCTL_BIN to a built agentctl"
	log "building agentctl"
	(cd "${root}" && go build -o "${DEMO_DIR}/bin/agentctl" ./cmd/agentctl)
	AGENTCTL_BIN=${DEMO_DIR}/bin/agentctl
fi
if [[ ${DEMO_AGENT} == fakeagent && -z ${FAKEAGENT_BIN:-} ]]; then
	need go "or set FAKEAGENT_BIN to a built fakeagent"
	log "building fakeagent"
	(cd "${root}" && go build -o "${DEMO_DIR}/bin/fakeagent" ./cmd/fakeagent)
	FAKEAGENT_BIN=${DEMO_DIR}/bin/fakeagent
fi
ac() { "${AGENTCTL_BIN}" "$@"; }

# ---- environment ------------------------------------------------------------
unset TMUX TMUX_PANE AGENTCTL_PAGER
export AGENTCTL_HOME=${DEMO_DIR}/agentctl-home
export PAGER=cat
if [[ ${DEMO_AGENT} == fakeagent ]]; then
	# fully hermetic: nothing reads the real home
	export HOME=${DEMO_DIR}/home XDG_CONFIG_HOME=${DEMO_DIR}/xdg-config XDG_STATE_HOME=${DEMO_DIR}/xdg-state
	export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
	mkdir -p "${HOME}"
	export AGENTCTL_AGENT_CMD="[\"/usr/bin/env\",\"FAKEAGENT_WORK_MS=1500\",\"FAKEAGENT_LOG=${DEMO_DIR}/fakeagent-input.jsonl\",\"AGENTCTL_DELIVER_SETTLE=50ms\",\"${FAKEAGENT_BIN}\"]"
else
	# real claude needs your login (~/.claude); agentctl state stays in DEMO_DIR
	unset AGENTCTL_AGENT_CMD
fi

repo=${DEMO_DIR}/demo-repo
mkdir -p "${repo}"
git -C "${repo}" init -q -b main
printf '# demo repo\n\nA throwaway repository for the agentctl demo.\n' >"${repo}/README.md"
printf '#!/bin/sh\necho "hello, ${1:-world}"\n' >"${repo}/greet.sh"
chmod +x "${repo}/greet.sh" # beta's goal runs ./greet.sh; without +x the shell refuses it
git -C "${repo}" add README.md greet.sh
git -C "${repo}" -c user.name=agentctl-demo -c user.email=demo@agentctl.invalid commit -q -m "initial"
cd "${repo}"

tm new-session -d -s demo-shell -c "${repo}" -x 200 -y 50
socket=$(tm display-message -p -t demo-shell '#{socket_path}')
export AGENTCTL_TMUX_SOCKET=${socket}
# the popup (prefix a) runs with the server's global environment
for v in AGENTCTL_HOME AGENTCTL_TMUX_SOCKET PATH HOME; do
	tm set-environment -g "${v}" "${!v}"
done
if [[ -n ${AGENTCTL_AGENT_CMD:-} ]]; then
	tm set-environment -g AGENTCTL_AGENT_CMD "${AGENTCTL_AGENT_CMD}"
fi
ac tmux-binding >"${DEMO_DIR}/binding.conf"
tm source-file "${DEMO_DIR}/binding.conf"
log "agent ${DEMO_AGENT} · repo ${repo} · tmux -L ${DEMO_SOCKET} (${socket}) · AGENTCTL_HOME ${AGENTCTL_HOME}"

# ---- helpers ----------------------------------------------------------------
state_of() { ac ls 2>/dev/null | awk -v n="$1" '$1 == n { print $3 }'; }
show_field() { ac show "$1" 2>/dev/null | awk -v f="$2" '$1 == f { $1 = ""; sub(/^ +/, ""); print; exit }'; }

# wait_for SECONDS DESCRIPTION COMMAND...: poll COMMAND until it succeeds.
wait_for() {
	local limit=$1 what=$2 start
	shift 2
	start=$(date +%s)
	until "$@"; do
		if (($(date +%s) - start >= limit)); then
			return 1
		fi
		sleep 0.2
	done
}
is_state() { [[ $(state_of "$1") == "$2" ]]; }
settled() {
	local s
	s=$(state_of "$1")
	if [[ ${s} == awaiting_input && ${DEMO_AGENT} == claude && ${hinted} != *" $1:${s} "* ]]; then
		hinted+=" $1:${s} "
		log "$1 is waiting for you (permission prompt?): tmux -L ${DEMO_SOCKET} attach -t agentctl-$1, answer it, then detach (prefix d)"
	fi
	if [[ ${s} == unknown && ${DEMO_AGENT} == claude && ${hinted} != *" $1:${s} "* ]]; then
		hinted+=" $1:${s} "
		if [[ $(show_field "$1" detail) == "folder trust: attach to answer" ]]; then
			# agentctl spots the dialog within one poll and never answers it
			log "$1 is on Claude's folder-trust dialog: tmux -L ${DEMO_SOCKET} attach -t agentctl-$1, choose \"Yes, I trust this folder\", then detach (prefix d)"
		else
			log "$1 is unknown (no hook within 30s): login dialog? tmux -L ${DEMO_SOCKET} attach -t agentctl-$1"
		fi
	fi
	[[ ${s} == ready ]]
}
hinted="" # bash 3.2 (macOS): no associative arrays
acked() { [[ $(show_field "$1" messages) == *"$2 acknowledged"* ]]; }
pane_has() { tm capture-pane -p -J -t "$1" | grep -qF -- "$2"; }
pane_dead() { [[ $(tm display-message -p -t "$1" '#{pane_dead}') == 1 ]]; }

# ---- 1. two agents in two worktrees ------------------------------------------
if [[ ${DEMO_AGENT} == claude ]]; then
	goal_alpha="Create a file NOTES.md containing one sentence that describes greet.sh. Do nothing else."
	goal_beta="Run ./greet.sh demo with Bash and reply with its output in one line. Do not edit any file."
else
	goal_alpha="outline the notes"
	goal_beta="check the greeting"
fi
log "1. new alpha and beta"
ac new alpha --goal "${goal_alpha}"
ac new beta --goal "${goal_beta}"
# ready from SessionStart would be premature (the goal is not submitted yet);
# agentctl keeps a goal-launched agent starting until then, and this wait
# insists on a ready that is not SessionStart's (m3-goal-start-race)
first_turn_done() { settled "$1" && [[ $(show_field "$1" detail) != "session started"* ]]; }
for a in alpha beta; do
	if wait_for "${DEMO_TIMEOUT}" "${a} ready" first_turn_done "${a}"; then
		log "${a}: ready after its first turn"
	else
		check_failed "${a} did not finish its first turn within ${DEMO_TIMEOUT}s (state $(state_of "${a}"))"
	fi
done
wt_alpha=$(show_field alpha worktree)
wt_beta=$(show_field beta worktree)
if [[ -z ${wt_alpha} || ${wt_alpha} == "${wt_beta}" || ! -d ${wt_alpha} || ! -d ${wt_beta} ]]; then
	check_failed "alpha and beta are not in two worktrees (${wt_alpha} / ${wt_beta})"
fi

# ---- 2. prompts: deliver-or-queue, acknowledgements ---------------------------
log "2. a multi-line prompt and a follow-up to alpha"
long_prompt="Reply with just the word OK. The lines below are padding to make this a large paste."
for i in $(seq 1 12); do
	long_prompt+=$'\n'"padding line ${i}: the quick brown fox jumps over the lazy dog, again and again."
done
out1=$(ac send alpha "${long_prompt}")
log "send #1: ${out1}"
out2=$(ac send alpha "Reply with just the word DONE.")
log "send #2: ${out2}"
if [[ ${out2} != *queued* && ${out2} != *sent* ]]; then
	check_failed "unexpected send label: ${out2}"
fi
if wait_for "${DEMO_TIMEOUT}" "two acknowledgements" acked alpha 2; then
	log "alpha: both prompts acknowledged"
else
	check_failed "alpha's prompts were not both acknowledged within ${DEMO_TIMEOUT}s (messages: $(show_field alpha messages))"
fi
wait_for "${DEMO_TIMEOUT}" "alpha ready" settled alpha || check_failed "alpha did not finish the prompted turns"

# ---- 3. diff and recent output ---------------------------------------------------
log "3. diff and recent output"
if [[ ${DEMO_AGENT} == fakeagent ]]; then
	printf 'Notes written in the alpha worktree.\n' >"${wt_alpha}/NOTES.md"
	printf '\nedited by alpha\n' >>"${wt_alpha}/README.md"
fi
diff_stat=$(ac diff alpha --stat)
printf '%s\n' "${diff_stat}"
if [[ ${diff_stat} != *NOTES.md* ]]; then
	check_failed "alpha's diff does not list NOTES.md"
fi
ac show alpha | tail -n 12

# ---- 4. the TUI in a pane: open, d (diff), esc, q ------------------------------------
log "4. the TUI: open, d, esc, q"
ui_env=()
for v in AGENTCTL_HOME AGENTCTL_TMUX_SOCKET PATH HOME PAGER; do
	ui_env+=(-e "${v}=${!v}")
done
for v in XDG_CONFIG_HOME XDG_STATE_HOME AGENTCTL_AGENT_CMD; do
	if [[ -n ${!v:-} ]]; then ui_env+=(-e "${v}=${!v}"); fi
done
tm new-session -d -s demo-ui -c "${repo}" -x 120 -y 40 "${ui_env[@]}" -e TERM=xterm-256color -- "${AGENTCTL_BIN}" tui \; \
	set-option -w remain-on-exit on
if wait_for 30 "the TUI header" pane_has demo-ui:. "agents / demo-repo"; then
	tm send-keys -t demo-ui:. d
	wait_for 30 "the diff view" pane_has demo-ui:. "vs main @" || check_failed "the TUI diff view did not open"
	tm send-keys -t demo-ui:. Escape
	wait_for 30 "the list" pane_has demo-ui:. "↵ open" || check_failed "esc did not return to the list"
	tm send-keys -t demo-ui:. q
	wait_for 30 "the TUI to exit" pane_dead demo-ui:. || check_failed "q did not quit the TUI"
	ui_status=$(tm display-message -p -t demo-ui:. '#{pane_dead_status}')
	[[ ${ui_status} == 0 ]] || check_failed "the TUI exited with status ${ui_status}"
else
	check_failed "the TUI did not draw its header"
fi
tm kill-session -t demo-ui

# ---- 5. stats -------------------------------------------------------------------
log "5. agentctl stats"
stats=$(ac stats)
printf '%s\n' "${stats}"
median_line=$(grep -F 'open → first action (median)' <<<"${stats}" || true)
if [[ -z ${median_line} || ${median_line} == *—* ]]; then
	check_failed "agentctl stats printed no median"
fi

# ---- 6. findings (before close: the panes are still there) ------------------------
log "6. findings"
for a in alpha beta; do
	cmd=$(tm display-message -p -t "=agentctl-${a}:" '#{pane_current_command}' 2>/dev/null || echo "?")
	printf 'pane_current_command %s: %s\n' "${a}" "${cmd}"
	if [[ ${DEMO_AGENT} == claude && ${cmd} != claude ]]; then
		finding "pane_current_command of a claude pane is '${cmd}', not 'claude' (adopt picker: AGENTCTL_ADOPT_COMMANDS)"
	fi
done
db=${AGENTCTL_HOME}/agentctl.db
if command -v sqlite3 >/dev/null 2>&1; then
	q() { sqlite3 "${db}" "$1" || finding "sqlite3 query failed: $1"; }
	echo "hook events received:"
	q "SELECT '  ' || ev || ' x' || n FROM (SELECT json_extract(payload,'$.event') AS ev, COUNT(*) AS n FROM events WHERE type='hook.received' GROUP BY ev ORDER BY ev);"
	echo "state changes:"
	q "SELECT '  ' || task_id || ': ' || group_concat(json_extract(payload,'$.from') || '>' || json_extract(payload,'$.to'), ' ') FROM (SELECT * FROM events WHERE type='agent.state_changed' ORDER BY id) GROUP BY task_id ORDER BY task_id;"
	echo "messages:"
	q "SELECT '  #' || id || ' ' || task_id || ' ' || state || ' ack=' || COALESCE(ack_match,'-') || ' ' || length(body) || ' bytes' FROM messages ORDER BY id;"
	orphans=$(q "SELECT COUNT(*) FROM events WHERE type='hook.orphan';")
	echo "orphan hooks: ${orphans}"
	if [[ ${orphans} != 0 ]]; then finding "${orphans} orphan hook(s) were recorded"; fi
	timing=$(q "SELECT COUNT(*) FROM messages WHERE ack_match='timing';")
	if [[ ${timing} != 0 ]]; then
		finding "${timing} prompt(s) acknowledged by timing: UserPromptSubmit.prompt did not carry the pasted text (placeholder?)"
	fi
else
	echo "(sqlite3 not found: inspect ${db} by hand)"
	grep -h 'acknowledged\|unconfirmed' "${AGENTCTL_HOME}/logs/deliver.log" 2>/dev/null | sed 's/^/  /' || true
fi
if [[ -s ${AGENTCTL_HOME}/logs/hook-errors.log ]]; then
	finding "hook-errors.log is not empty:"
	sed 's/^/  /' "${AGENTCTL_HOME}/logs/hook-errors.log"
fi

if [[ ${DEMO_PAUSE} == 1 ]]; then
	cat <<EOF

Manual checks (the agents are still running):
  tmux -L ${DEMO_SOCKET} attach -t demo-shell
  1. popup: press prefix a (the agentctl binding is installed on this server);
     select beta, enter, enter. Do you land on agentctl-beta's pane when the
     popup closes? (open question: switch-client from display-popup)
  2. hooks: did your own ~/.claude or project hooks still run next to the
     --settings ones? (open question: --settings merge)
  3. permission: in the popup, does a permission prompt show in NEEDS YOU with
     its detail, and does p queue "awaiting input" without pasting?
  Detach with prefix d, then press Enter here to close the agents and clean up.
EOF
	read -r _ </dev/tty || true
fi

# ---- 7. close: stop alpha, keep beta running; nothing is deleted --------------------
log "7. close alpha --stop, close beta"
ac close alpha --stop --yes
ac close beta
if tm has-session -t "=agentctl-alpha" 2>/dev/null; then
	check_failed "close --stop left alpha's session running"
fi
if ! tm has-session -t "=agentctl-beta" 2>/dev/null; then
	check_failed "a plain close stopped beta's agent"
fi
for wt in "${wt_alpha}" "${wt_beta}"; do
	[[ -d ${wt} ]] || check_failed "worktree ${wt} was deleted"
done
branches=$(git -C "${repo}" branch --list 'agentctl/*')
for a in alpha beta; do
	[[ ${branches} == *"agentctl/${a}"* ]] || check_failed "branch agentctl/${a} was deleted"
done
ac ls --closed

echo
echo "FINDINGS (${DEMO_AGENT}):"
if ((${#findings[@]} == 0)); then
	echo "  none"
else
	printf '  - %s\n' "${findings[@]}"
fi
if [[ ${DEMO_AGENT} == fakeagent ]]; then
	log "OK"
else
	log "done (${failures} failed checks, ${#findings[@]} findings)"
fi
