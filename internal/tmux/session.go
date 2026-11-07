package tmux

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// createdFormat is printed by new-session -P. Only fixed-shape fields, so tab
// separation is unambiguous.
const createdFormat = "#{session_id}\t#{pane_id}\t#{pane_pid}"

// NewSessionSpec describes a detached session running one command.
type NewSessionSpec struct {
	Name string   // session name; tmux forbids ':' and '.'
	Dir  string   // absolute start directory (format-escaped)
	Env  []string // KEY=VALUE pairs passed with -e
	Argv []string // command; >= 2 elements so tmux execs it directly (never sh -c)
}

// Created is the identity tmux reports for a new session's first pane.
type Created struct {
	SessionID string // "$N"
	PaneID    string // "%N"
	PanePID   int
}

// NewSessionCommands builds the single invocation that creates a detached
// session and turns remain-on-exit on for its window before the server can
// reap an instantly-exiting agent:
//
//	new-session -d -P -F '<fmt>' -s <name> -c <dir> -e K=V ... -- <argv...> ; set-option -w remain-on-exit on
//
// tmux execs a multi-element command directly, so argv elements (the goal in
// particular) never pass through a shell. A one-element command would go to
// `sh -c` and is rejected.
func NewSessionCommands(spec NewSessionSpec) ([][]string, error) {
	if spec.Name == "" || strings.ContainsAny(spec.Name, ":.") {
		return nil, fmt.Errorf("tmux: invalid session name %q", spec.Name)
	}
	if spec.Dir == "" || !filepath.IsAbs(spec.Dir) {
		return nil, fmt.Errorf("tmux: start directory %q is not absolute", spec.Dir)
	}
	if len(spec.Argv) < 2 {
		return nil, errors.New("tmux: command needs at least 2 argv elements (a single element would run through sh -c)")
	}
	for _, a := range spec.Argv {
		if strings.IndexByte(a, 0) >= 0 {
			return nil, errors.New("tmux: argv element contains NUL")
		}
	}
	cmd := []string{"new-session", "-d", "-P", "-F", createdFormat, "-s", spec.Name, "-c", EscapeFormat(spec.Dir)}
	for _, kv := range spec.Env {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" || strings.IndexByte(kv, 0) >= 0 {
			return nil, fmt.Errorf("tmux: invalid environment entry %q", kv)
		}
		cmd = append(cmd, "-e", kv)
	}
	cmd = append(cmd, "--")
	cmd = append(cmd, spec.Argv...)
	return [][]string{cmd, {"set-option", "-w", "remain-on-exit", "on"}}, nil
}

// NewSession creates the session and returns its pane identity. tmux exits 0
// with no output when it cannot even create the socket, so missing output is
// an error too.
func (c Client) NewSession(ctx context.Context, spec NewSessionSpec) (Created, error) {
	cmds, err := NewSessionCommands(spec)
	if err != nil {
		return Created{}, err
	}
	res, err := c.run(ctx, nil, cmds...)
	if err != nil {
		return Created{}, err
	}
	created, perr := parseCreated(res.Stdout)
	if perr != nil {
		if msg := firstLine(string(res.Stderr)); msg != "" {
			return Created{}, newError(commandNames(cmds), msg, perr)
		}
		return Created{}, perr
	}
	return created, nil
}

func parseCreated(out []byte) (Created, error) {
	line := strings.TrimSpace(string(out))
	f := strings.Split(line, "\t")
	if len(f) != 3 || !strings.HasPrefix(f[0], "$") || !strings.HasPrefix(f[1], "%") {
		return Created{}, fmt.Errorf("tmux: unexpected new-session output %q", line)
	}
	pid, err := strconv.Atoi(f[2])
	if err != nil || pid <= 0 {
		return Created{}, fmt.Errorf("tmux: unexpected pane pid in %q", line)
	}
	return Created{SessionID: f[0], PaneID: f[1], PanePID: pid}, nil
}

// HasSessionCommand checks for an exact session name (the '=' prefix disables
// tmux's prefix matching).
func HasSessionCommand(name string) []string { return []string{"has-session", "-t", "=" + name} }

// HasSession reports whether a session with exactly this name exists. A
// missing server counts as "no".
func (c Client) HasSession(ctx context.Context, name string) (bool, error) {
	_, err := c.run(ctx, nil, HasSessionCommand(name))
	if err == nil {
		return true, nil
	}
	var te *Error
	if errors.Is(err, ErrNoServer) || (errors.As(err, &te) && strings.HasPrefix(te.Stderr, "can't find session")) {
		return false, nil
	}
	return false, err
}
