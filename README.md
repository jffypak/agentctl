# agentctl

One popup that tells you which of your coding agents needs you, and lets you
answer it without hunting through tmux.

agentctl runs Claude Code agents in their own git worktrees and tmux sessions,
tracks each agent's state through Claude Code's lifecycle hooks, and shows them
in a tmux popup sorted by attention: **NEEDS YOU** (permission prompts,
crashes, lost panes, unconfirmed messages), **IDLE** (turn finished),
**RUNNING**, **UNKNOWN**. Prompts you send are delivered only when the agent is
ready, and queued otherwise. They are never pasted into a running turn or a
permission dialog.

Phase 1 (session manager). Requirements: macOS or Linux, tmux ≥ 3.3, git ≥ 2.40,
Go 1.27 to build, and [Claude Code](https://code.claude.com) on `PATH`.

## Quickstart

**1. Install**

```sh
go install github.com/jffypak/agentctl/cmd/agentctl@latest   # or: make build  ->  bin/agentctl
```

**2. Add the tmux binding.** It opens agentctl in a popup with `prefix a`:

```sh
agentctl tmux-binding >> ~/.tmux.conf    # bind-key a display-popup -E … '/abs/path/agentctl'
tmux source-file ~/.tmux.conf
```

**3. Start an agent** from inside the repository, in tmux:

```sh
cd ~/src/myrepo
agentctl new auth-tests --goal "Write integration tests for the OAuth login flow"
```

This creates the worktree `../myrepo-worktrees/auth-tests` on the branch
`jpark/auth-tests`. It starts `claude --settings <agentctl hooks> "<goal>"` in
the tmux session `agentctl-auth-tests`, and the agent shows as `starting`. You
can also press `n` in the popup to do this. The first time Claude Code opens a
new worktree it asks whether you trust the folder, and no hooks fire until you
answer. agentctl spots the dialog within one poll and lists the agent under
NEEDS YOU as `folder trust: attach to answer`, but it never answers for you:
attach, accept it if you trust the folder, and detach.

**4. Open the popup** with `prefix a`, or run `agentctl` for the full-screen TUI:

```
 agents / myrepo · 1 running · 1 need you
 NEEDS YOU
 > auth-tests   awaiting input · permission prompt: Bash(npm test)   4m
 RUNNING
   docs-site    working · Bash: go test ./...                      45s
```

| Key | Action |
|---|---|
| `↑↓` / `j k` | Move between agents |
| `↵` | Open the detail view (state, worktree, diff summary, recent output). `↵` again attaches: the popup closes and you land on the agent's pane. Detach to come back. |
| `l` | Recent output. `f` shows the full scrollback in your pager. |
| `/` | Filter. Type `closed` to list closed tasks. |
| `q` / `esc` | Back, or quit. Quitting never stops an agent. |

**5. Send a prompt.** Select the agent, press `p`, type, and press `ctrl+s`. The message
shows `sent · awaiting ack` when it was pasted, or `queued · agent working`
(or `awaiting input`) when it was not. It goes in after the agent's turn ends.
Once Claude Code confirms the prompt, it shows `acknowledged`. From a shell:

```sh
agentctl send auth-tests "Also cover the expired-token path"
agentctl send --cancel 7           # cancel a message that is still queued
```

**6. Review the diff.** Press `d` in the popup to list changed files, then `↵`
to page the full diff. From a shell:

```sh
agentctl diff auth-tests --stat    # committed / staged / unstaged / untracked / conflicts vs the base
agentctl diff auth-tests           # full diff (pager on a terminal)
agentctl show auth-tests           # state block + last 40 lines of output
```

**7. Close the task.** Press `x` then `y` in the popup, or run:

```sh
agentctl close auth-tests          # hides the task; the agent keeps running
agentctl close auth-tests --stop   # also stops the agent's tmux session (asks y/N)
```

Closing never deletes anything. The worktree and the branch stay, and you merge
or remove them with git yourself.

**How long does triage take?** `agentctl stats` prints the median time from
opening the popup to your first action (the target is under 60 s), and attaches
per prompt sent.

### Dashboard pane

To keep agentctl on screen all the time, run it in its own pane with `--stay`:

```sh
tmux split-window -h -l 35% 'agentctl tui --stay'
```

With `--stay`, `↵` on an agent still moves you to the agent's pane, but
agentctl does not exit. Press the tmux key for `switch-client -l` (last
session) to come back to the dashboard. The tmux default is `prefix L`.
agentctl reads your key from tmux and shows it in the hint after each switch.
Press `q` in the list to quit.

### Other commands

`agentctl ls [--json] [--closed]` lists tasks by attention section.
`agentctl adopt` lists running claude panes that agentctl doesn't track yet, and
`agentctl adopt %7 --name x` registers one. An adopted pane shows `unknown`
until its hooks report. To enable them, merge `agentctl hooks-snippet` into that
agent's Claude settings. `agentctl help` lists everything.

### Codex

agentctl also tracks [Codex CLI](https://learn.chatgpt.com/docs) agents, with
the same sections, prompt queue and delivery rules as Claude.

- `agentctl new <name> --agent codex --goal "<goal>"` starts Codex in a new
  worktree and tmux session, like Claude. In the popup, `n` has an agent field
  (`←/→` switches claude ⇄ codex).
- `agentctl codex [--name <name>] [codex args…]` runs Codex in the pane you are
  in and tracks it. It registers the pane, runs Codex with agentctl's hooks,
  and records when Codex exits. The pane, your shell, stays open. Run it again
  in the same pane to start a new session on the same task.

Codex asks you to trust new hooks once. The first time, Codex shows a "Review
hooks" screen and the agent is listed under NEEDS YOU as `hook trust: attach to
answer`. Attach and pick "Trust all and continue". agentctl never answers it.

A Codex you start with plain `codex` can't be tracked: Codex then runs its
hooks in a shared background server, which reports the wrong tmux pane.
`agentctl adopt` says so for such a pane. Start it with `agentctl codex`
instead. agentctl's hooks are passed with `-c`, which can replace your own
Codex hooks for the same events in that session. `AGENTCTL_CODEX_CMD`
overrides the Codex command (a JSON array, default `["codex"]`).

A Codex started without a prompt sends no event until you type the first one.
Until then it is listed under INITIALSTATE as `waiting for first prompt ·
attach to start`.

### Where state lives

`$AGENTCTL_HOME`, else `$XDG_STATE_HOME/agentctl`, else
`~/.local/state/agentctl`. This holds `agentctl.db` (SQLite registry and events),
`logs/` (`hook-errors.log`, `deliver.log`, full captures) and
`hooks/claude-settings.json`. Key bindings can be overridden in
`$XDG_CONFIG_HOME/agentctl/config.yaml` (`keys: {diff: D}`).

## Troubleshooting

| You see | Do this |
|---|---|
| `tmux not found on PATH` | Install tmux 3.3 or newer (`brew install tmux` / your package manager). |
| `git not found on PATH` | Install git 2.40 or newer. |
| `… is not inside a git repository` | `cd` into the repository (or one of its worktrees) and run the command again. agentctl registers tasks against the main checkout. |
| `… has no commits yet` | Make an initial commit. Worktrees branch from `HEAD`. |
| `agent command "claude" not found on PATH` | Install Claude Code, or make `claude` reachable from the shell tmux starts. `AGENTCTL_AGENT_CMD='["/path/to/claude"]'` overrides the command. For Codex: install Codex CLI, or set `AGENTCTL_CODEX_CMD`. |
| `branch jpark/<name> already exists` | Pick another task name, or delete the old branch yourself if you are done with it (agentctl never deletes branches). |
| `worktree path … already exists` | Pick another task name, or move that directory out of `../<repo>-worktrees/`. |
| `tmux session agentctl-<name> already exists` | Pick another name, or close that session. |
| `task "<name>" already exists` | Names are unique per registry. `agentctl ls --closed` lists every task. |
| `cannot open registry …` | The banner names the path. Check its permissions, or point `AGENTCTL_HOME` at a writable directory. |
| `silent since launch · attach to check` with `folder trust: attach to answer` | Claude is showing its folder-trust dialog (each new worktree is a new path). agentctl never answers it: attach (`↵ ↵`), pick "Yes, I trust this folder" if you do, then detach. Prompts sent meanwhile stay queued. |
| Agent stays `starting`, then `silent since launch · attach to check` | No hook arrived within 30 s and no folder-trust dialog was seen. Attach (`↵ ↵`): Claude is usually waiting on its login dialog. |
| INITIALSTATE · `no events yet` | The agent has not sent its first hook event. For an adopted pane, no hooks are installed: merge `agentctl hooks-snippet` into its Claude settings, or attach and type the prompt yourself. A Codex agent sends nothing until its first prompt. |
| `hook trust: attach to answer` | Codex is showing its hook-trust screen. agentctl never answers it: attach, pick "Trust all and continue" if you trust agentctl's hooks, then detach. You trust them once. |
| `codex not running in pane · attach to check` | The pane of an `agentctl codex` task no longer runs Codex, so nothing is pasted. Run `agentctl codex` in that pane again. |
| `agentctl codex is not running inside pane …` / `AGENTCTL_TMUX_SOCKET names a different tmux server` | `agentctl codex` only tracks the pane it runs in, on that pane's own tmux server. Run it directly in the tmux pane you want tracked, and unset `AGENTCTL_TMUX_SOCKET` if it points elsewhere. |
| `agentctl codex runs the interactive Codex TUI` | `agentctl codex` only wraps the TUI (`codex`, `codex resume`, `codex fork`). Run other subcommands such as `codex exec` directly; they are not tracked. |
| `unknown` on an adopted agent; prompts say `queued · attach to deliver` | Its hooks reported before, but the state was lost. The next hook fixes it. Until then attach and type the prompt yourself. |
| `disconnected · pane_missing` / `identity_mismatch` / `server_gone` | The pane was killed, or the tmux server restarted and reused its id. agentctl never re-attributes it to another pane. You can still inspect it with `d`, or close it with `x`. Start a new task to continue. |
| `message unconfirmed · attach to check` | A prompt was pasted but Claude Code never confirmed it within 15 s. Attach and look. It is never pasted again automatically. |
| `queued · state stale — attach to deliver` | A hook could not be recorded, so the state may be wrong. The next recorded hook clears this. Until then nothing is pasted. |
| `attach refused: …` | The pane is gone or reused. agentctl will not focus a pane it cannot verify. |
| `refusing to stop …` | `--stop` only kills a session agentctl launched and can still verify. Plain `close` always works. |
| Hook errors | `agentctl hook` never blocks Claude: it always exits 0 and logs to `<home>/logs/hook-errors.log`. |

## Development

```sh
scripts/doctor.sh                           # the gate: gofmt, go vet, go test -race ./..., go mod tidy -diff
go test -race -count=10 ./internal/e2e      # Phase-1 acceptance tests (real TUI, tmux, git, fakeagent)
DEMO_AGENT=fakeagent scripts/demo.sh        # the two-agent demo, hermetic (also run by go test)
DEMO_AGENT=claude scripts/demo.sh           # the same with real Claude Code (by hand; uses your login)
```

Tests never touch your tmux server, `~/.claude` or `$HOME`. Every test gets an
isolated `tmux -L agentctl-test-<rand>` server, a temp repo and a temp
`AGENTCTL_HOME`. The demo uses `tmux -L agentctl-demo`, so you can watch it
with `tmux -L agentctl-demo attach`. Design, invariants and open questions are in
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). The map from acceptance criteria
and invariants to tests is in
[docs/notes/m3-acceptance-e2e.md](docs/notes/m3-acceptance-e2e.md).
