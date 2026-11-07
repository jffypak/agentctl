package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
)

// command is one subcommand. The table below is complete from day one; a card
// implements a subcommand by rewriting its cmd_<name>.go stub whole and never
// edits this file.
type command struct {
	name    string
	summary string
	run     func(ctx context.Context, sys *system, args []string) int
}

var commands = []command{
	{"tui", "open the attention-sorted TUI (same as bare agentctl)", cmdTUI},
	{"new", "new <name> --goal <goal>: worktree + tmux session running the agent", cmdNew},
	{"adopt", "adopt [%pane --name <name>]: register an existing agent pane", cmdAdopt},
	{"ls", "ls [--json] [--closed]: list tasks by attention section", cmdLs},
	{"show", "show <name>: task, session, identity, state, recent output", cmdShow},
	{"diff", "diff <name>: diff against the task's base", cmdDiff},
	{"send", "send <name> <message>: deliver or queue a prompt", cmdSend},
	{"close", "close <name> [--stop]: hide a task (worktree and branch kept)", cmdClose},
	{"hook", "hook: Claude Code hook entry point (reads stdin, prints nothing)", cmdHook},
	{"deliver", "deliver --session <id>: background delivery after a hook", cmdDeliver},
	{"stats", "stats [--json]: time from popup open to first action", cmdStats},
	{"hooks-snippet", "hooks-snippet: hooks JSON to merge into your Claude settings", cmdHooksSnippet},
	{"tmux-binding", "tmux-binding: tmux.conf line that opens the popup", cmdTmuxBinding},
}

// run dispatches args (without the program name) and returns the exit code.
func run(ctx context.Context, args []string, sys *system) int {
	if len(args) == 0 {
		return cmdTUI(ctx, sys, nil)
	}
	switch args[0] {
	case "help", "-h", "-help", "--help":
		printUsage(sys.stdout)
		return exitOK
	}
	for _, c := range commands {
		if c.name == args[0] {
			return c.run(ctx, sys, args[1:])
		}
	}
	fmt.Fprintf(sys.stderr, "agentctl: unknown command %q (run `agentctl help`)\n", args[0])
	return exitUsage
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: agentctl [command] [flags]")
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(tw, "  %s\t%s\n", c.name, c.summary)
	}
	_ = tw.Flush()
}

// stubs maps each still-stubbed subcommand to the issue that owns it. Every
// stub file registers itself (`var _ = registerStub(...)`), so rewriting the
// file whole removes the entry; tests check exactly the stubs that remain.
var stubs = map[string]string{}

func registerStub(name, issue string) bool {
	stubs[name] = issue
	return true
}

// notImplemented is what every stub subcommand returns until its card lands.
// It writes only to stderr (stdout must stay empty for `hook`).
func notImplemented(sys *system, name, issue string) int {
	fmt.Fprintf(sys.stderr, "agentctl %s: not implemented yet (%s)\n", name, issue)
	return exitUsage
}

// newFlagSet returns a ContinueOnError flag set writing to stderr.
func newFlagSet(sys *system, name string) *flag.FlagSet {
	fs := flag.NewFlagSet("agentctl "+name, flag.ContinueOnError)
	fs.SetOutput(sys.stderr)
	return fs
}

// parseInterspersed parses flags that may appear before or after positional
// arguments (`agentctl new demo --goal x`); "--" ends flag parsing.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// flagExit maps a flag parse error to an exit code (-h is not an error).
func flagExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	return exitUsage
}
