package controller

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// claudeAdoptCommand is the adopt-command name that means "Claude Code,
// however tmux names it". tmux's pane_current_command is the executable's
// name, and Claude Code's launcher (~/.local/bin/claude) is a symlink into
// claude/versions/<ver>, so a real Claude pane reports e.g. "2.1.319". When
// "claude" is among the adopt commands, panes that fail the exact name match
// are resolved through the process table (one ps per listing).
const claudeAdoptCommand = "claude"

// claudeVersionsSegment is the path shape of Claude Code's versioned binaries.
const claudeVersionsSegment = "/claude/versions/"

// psTimeout bounds the one process-table read per adopt listing.
const psTimeout = 5 * time.Second

// psArgs prints the whole process table, portable across procps (Linux) and
// BSD ps (macOS): no headers, unlimited width, args last (it has spaces).
var psArgs = []string{"-A", "-ww", "-o", "pid=", "-o", "ppid=", "-o", "pgid=", "-o", "tpgid=", "-o", "tty=", "-o", "args="}

// jsRuntimes may run Claude Code as a script (`node /usr/local/bin/claude`).
var jsRuntimes = map[string]bool{"node": true, "bun": true}

// proc is one row of the process table.
type proc struct {
	PID, PPID, PGID int
	TPGID           int    // foreground process group of the controlling terminal (<= 0: none)
	TTY             string // controlling terminal as ps prints it; "" when none
	Args            string // command line, space-joined
}

// procLister reads the process table.
type procLister func(ctx context.Context) ([]proc, error)

// procTable indexes a process table.
type procTable struct {
	procs []proc
	byPID map[int]proc
}

func newProcTable(procs []proc) procTable {
	byPID := make(map[int]proc, len(procs))
	for _, p := range procs {
		byPID[p.PID] = p
	}
	return procTable{procs: procs, byPID: byPID}
}

// paneRunsClaude reports whether the foreground of the pane whose process is
// panePID is Claude Code (any process of its foreground group).
func paneRunsClaude(t procTable, panePID int) bool {
	for _, p := range t.foreground(panePID) {
		if isClaudeArgs(p.Args) {
			return true
		}
	}
	return false
}

// foreground returns the foreground process group of the terminal panePID
// controls (the processes on its tty whose pgid is the tty's tpgid). When ps
// reports no foreground group it falls back to panePID and its descendants.
func (t procTable) foreground(panePID int) []proc {
	leader, ok := t.byPID[panePID]
	if !ok {
		return nil
	}
	if leader.TTY != "" && leader.TPGID > 0 {
		var out []proc
		for _, p := range t.procs {
			if p.TTY == leader.TTY && p.PGID == leader.TPGID {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return t.descendants(leader)
}

// descendants returns root and every process below it.
func (t procTable) descendants(root proc) []proc {
	out := []proc{root}
	seen := map[int]bool{root.PID: true}
	for i := 0; i < len(out); i++ {
		for _, p := range t.procs {
			if p.PPID == out[i].PID && !seen[p.PID] {
				seen[p.PID] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// isClaudeArgs reports whether a command line is Claude Code: argv[0]'s
// basename is "claude" or argv[0] sits under a claude/versions/ directory, or
// a JS runtime (node, bun) runs such a path as argv[1].
func isClaudeArgs(args string) bool {
	argv := strings.Fields(args)
	if len(argv) == 0 {
		return false
	}
	if isClaudePath(argv[0]) {
		return true
	}
	return len(argv) > 1 && jsRuntimes[path.Base(argv[0])] && isClaudePath(argv[1])
}

func isClaudePath(arg string) bool {
	return path.Base(arg) == "claude" || strings.Contains("/"+arg, claudeVersionsSegment)
}

// psProcs runs ps once and parses its table.
func (c *Controller) psProcs(ctx context.Context) ([]proc, error) {
	bin, err := c.lookPath("ps")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, psTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, psArgs...)
	cmd.Env = c.env
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(out)
}

// parsePS parses `ps -o pid=,ppid=,pgid=,tpgid=,tty=,args=` output.
func parsePS(out []byte) ([]proc, error) {
	var procs []proc
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		p, err := parsePSLine(line)
		if err != nil {
			return nil, err
		}
		procs = append(procs, p)
	}
	return procs, nil
}

func parsePSLine(line string) (proc, error) {
	const fixed = 5 // pid ppid pgid tpgid tty
	rest := line
	fields := make([]string, 0, fixed)
	for len(fields) < fixed {
		rest = strings.TrimLeft(rest, " \t")
		end := strings.IndexAny(rest, " \t")
		if rest == "" {
			return proc{}, fmt.Errorf("ps: unexpected line %q", line)
		}
		if end < 0 {
			end = len(rest)
		}
		fields, rest = append(fields, rest[:end]), rest[end:]
	}
	ints := make([]int, 4)
	for i := range ints {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			return proc{}, fmt.Errorf("ps: unexpected field %d in line %q", i, line)
		}
		ints[i] = n
	}
	return proc{
		PID: ints[0], PPID: ints[1], PGID: ints[2], TPGID: ints[3],
		TTY: normalizeTTY(fields[4]), Args: strings.TrimSpace(rest),
	}, nil
}

// normalizeTTY maps ps's "no terminal" spellings ("?" on Linux, "??" on
// macOS, "-") to "".
func normalizeTTY(tty string) string {
	if strings.Trim(tty, "?-") == "" {
		return ""
	}
	return tty
}
