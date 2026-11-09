# m3-adopt-claude-match — recognise real Claude panes in the adopt picker

## Problem

tmux's `#{pane_current_command}` is the foreground process's executable name.
Claude Code's launcher `~/.local/bin/claude` is a symlink into
`~/.local/share/claude/versions/<ver>`, so on macOS a real Claude pane reports
`2.1.319` and `DefaultAdoptCommands = ["claude"]` matched nothing. (On Linux
the comm is the name exec'd through the symlink, `claude`, so the first pass
already matches there.)

## Rule (internal/controller/adopt_match.go)

1. **First pass (cheap):** `pane_current_command` is in the adopt-command list.
2. **Resolution:** only when the list contains `claude` *and* some live,
   unregistered pane failed the first pass, run `ps` **once**:
   `ps -A -ww -o pid= -o ppid= -o pgid= -o tpgid= -o tty= -o args=` (portable
   procps / BSD flags; `-ww` so args is never truncated).
3. **Foreground of a pane:** the row for `pane_pid` gives the pane's tty and the
   tty's foreground pgid (`tpgid`); the foreground processes are the rows on
   that tty with `pgid == tpgid`. If ps reports no tty/tpgid, fall back to
   `pane_pid` and its descendants. No `pane_tty` field was added to the tmux
   format (the shell's own row carries both).
4. **Claude test (`isClaudeArgs`):** argv[0] basename is `claude`, or argv[0]
   contains a `claude/versions/` path segment; or argv[0] is `node`/`bun` and
   argv[1] passes the same test. Any process in the foreground group may match
   (Claude's own tool children share its group). Not matched: `vim claude`,
   `claudette`, `.../notclaude/versions/...`, `less .../claude/versions/x`, a
   backgrounded claude, a claude on another tty.

The real `claude` argv[0] is `claude` (ps prints what the shell exec'd), which
is why the basename check alone covers the normal install.

## Env override

`AGENTCTL_ADOPT_COMMANDS` names keep exact `pane_current_command` semantics.
Resolution is tied to the name `claude`: `AGENTCTL_ADOPT_COMMANDS=sleep` never
runs ps; `claude, sleep` resolves Claude panes and exact-matches `sleep`.
`--all` never runs ps.

## Failure mode

A ps failure (missing binary, timeout 5 s, unparsable line) does not fail the
listing: it returns the exact-match result with `AdoptCandidates.Warning` set.
The CLI prints it as `agentctl: warning: …` on stderr; the TUI picker shows it
(error style, one line) in place of the socket line, so the header height is
unchanged.

## Tests

- `TestPaneRunsClaude` (table, 16 process-table cases), `TestIsClaudeArgs`,
  `TestParsePS` (macOS `??` / Linux `?` / `-` ttys, args with spaces, empty args,
  malformed lines) — pure, no processes.
- `TestAdoptCandidatesRecogniseVersionNamedClaude` (isolated tmux server): a
  copy of fakeagent at `<tmp>/claude/versions/9.9.9` launched through the
  symlink `<tmp>/bin/claude` (tmux reports `9.9.9` on macOS, exactly like real
  Claude) is listed; `<tmp>/other/8.8.8` (same binary, not under claude) and an
  interactive `/bin/sh` are not; the registered `demo` pane is not; ps runs
  exactly once per listing (counting wrapper over the real lister); an
  `AGENTCTL_ADOPT_COMMANDS=8.8.8` controller exact-matches without ps; a
  failing lister degrades with a warning; after adopt the pane is gone.
- `TestAdoptPickerShowsListingWarning` (TUI).

Why a copied fakeagent and not a script: macOS reports the *interpreter* as a
script's comm (`bash` for `/bin/sh`), and copies of system binaries such as
`/bin/sleep` are SIGKILLed (platform signature). A Go-built binary copies fine.

Checked by hand against the live tmux on the dev machine (two real Claude
2.1.319 panes): both detected, every zsh/nvim pane rejected.
