// Command fakeagent is a scripted Claude Code stand-in for tests
// (ARCHITECTURE.md §fakeagent contract). It is never shipped.
//
//	fakeagent --settings <file> [initial prompt]
//	fakeagent --codex [-c hooks.<Event>=…]... [initial prompt]   (Codex CLI mode: codex.go)
//
// It runs the command hooks in the settings file exactly as Claude Code would
// (payload JSON on stdin with session_id, hook_event_name, cwd and the event
// fields; inherited env), puts the tty in raw mode, enables bracketed paste
// (ESC[?2004h) and prints a "> " prompt. A submitted prompt fires
// UserPromptSubmit, "works" for FAKEAGENT_WORK_MS (default 300) firing
// PreToolUse/PostToolUse, then fires Stop and only then flips to ready.
//
// Directives inside a prompt: #ask (Bash permission dialog: PreToolUse,
// PermissionRequest, Notification permission_prompt; waits for y/Enter or n),
// #exit N (fires SessionEnd, exits N), #sleep MS (works MS instead).
//
// Environment:
//
//	FAKEAGENT_WORK_MS=300            turn duration
//	FAKEAGENT_LOG=<file>             JSONL {ts, text, state_at_receipt, kind} per received input
//	                                 (kind: paste | submit | key)
//	FAKEAGENT_HOOK_LOG=<file>        JSONL {ts, event, command, exit, stdout, stderr, ms} per hook run
//	FAKEAGENT_PASTE_PLACEHOLDER=1    report pastes to UserPromptSubmit as [Pasted text #N]
//	FAKEAGENT_TRUST_DIALOG=1         show a Claude-style folder-trust dialog before SessionStart and
//	                                 fire no hook until it is answered: 1/Enter accepts, 2/n exits 1
//	FAKEAGENT_HOOK_TRUST=1           Codex mode: show Codex's hook-trust screen first; 2/Enter trusts,
//	                                 3 continues without hooks, 1 is ignored
//	FAKEAGENT_GOAL_DELAY_MS=0        gap between SessionStart and submitting the initial prompt
//	                                 (input in the gap is received while starting)
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/term"
)

// defaultWork is the turn duration when FAKEAGENT_WORK_MS is unset.
const defaultWork = 300 * time.Millisecond

func main() {
	cfg, err := parseConfig(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeagent:", err)
		os.Exit(2)
	}
	fd := int(os.Stdin.Fd())
	restore := func() {}
	raw := false
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			raw = true
			restore = func() { _ = term.Restore(fd, old) }
		}
	}
	a, err := newAgent(cfg, os.Stdout, raw)
	if err != nil {
		restore()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	finish := func(code int) {
		a.term.write("\x1b[?2004l")
		restore()
		os.Exit(code)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigs
		a.end("other", 0)
		finish(128 + int(s.(syscall.Signal)))
	}()
	go a.readInput(os.Stdin)
	finish(a.run())
}

// parseConfig reads `--settings <file> [prompt]` and the FAKEAGENT_* env.
func parseConfig(args []string, getenv func(string) string) (config, error) {
	var codexHooks settings
	codexMode := len(args) > 0 && args[0] == "--codex"
	if codexMode {
		hooks, prompt, err := parseCodexArgs(args[1:])
		if err != nil {
			return config{}, err
		}
		codexHooks, args = hooks, nil
		if prompt != "" {
			args = []string{"--", prompt}
		}
	}
	fs := flag.NewFlagSet("fakeagent", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	settingsPath := fs.String("settings", "", "Claude Code settings file whose command hooks to run")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() > 1 {
		return config{}, errors.New("usage: fakeagent --settings <file> [initial prompt]")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return config{}, err
	}
	cfg := config{
		settingsPath: *settingsPath, initialPrompt: fs.Arg(0), work: defaultWork,
		logPath: getenv("FAKEAGENT_LOG"), hookLogPath: getenv("FAKEAGENT_HOOK_LOG"),
		pastePlaceholder: getenv("FAKEAGENT_PASTE_PLACEHOLDER") == "1",
		trustDialog:      getenv("FAKEAGENT_TRUST_DIALOG") == "1",
		codex:            codexMode, codexHooks: codexHooks,
		hookTrust: getenv("FAKEAGENT_HOOK_TRUST") == "1",
		cwd:       cwd, env: os.Environ(),
		transcriptDir: filepath.Join(os.TempDir(), "fakeagent-transcripts-never-created"),
	}
	if v := getenv("FAKEAGENT_WORK_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 0 {
			return config{}, fmt.Errorf("FAKEAGENT_WORK_MS=%q is not a non-negative integer", v)
		}
		cfg.work = time.Duration(ms) * time.Millisecond
	}
	if cfg.goalDelay, err = parseGoalDelay(getenv); err != nil {
		return config{}, err
	}
	return cfg, nil
}
