// Package tmux wraps the tmux CLI. Every call carries an explicit socket
// (-S absolute path in production, -L name only when testenv bootstraps an
// isolated server); an empty socket is an error, never "the default server".
// Commands are built by pure arg builders and executed through the Runner
// interface so tests can record or fake invocations.
//
// One file per command family: session.go (new-session, has-session),
// panes.go (list-panes snapshot), server.go (server pid, socket resolution).
// Later cards add capture.go, buffer.go, kill.go, attach.go, binding.go.
package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// Sentinel errors.
var (
	ErrNoSocket = errors.New("tmux: no socket given (every call needs an explicit -S path or -L name)")
	ErrNoServer = errors.New("tmux: no server running on socket")
)

// Socket names exactly one tmux server.
type Socket struct {
	Path string // -S: absolute socket path (what sessions store)
	Name string // -L: socket name under $TMUX_TMPDIR/tmux-<uid>/ (testenv bootstrap)
}

// IsZero reports whether no socket is set.
func (s Socket) IsZero() bool { return s.Path == "" && s.Name == "" }

func (s Socket) String() string {
	if s.Path != "" {
		return s.Path
	}
	return "-L " + s.Name
}

func (s Socket) flags() ([]string, error) {
	switch {
	case s.IsZero():
		return nil, ErrNoSocket
	case s.Path != "" && s.Name != "":
		return nil, fmt.Errorf("tmux: socket has both -S %q and -L %q", s.Path, s.Name)
	case s.Path != "" && !filepath.IsAbs(s.Path):
		return nil, fmt.Errorf("tmux: socket path %q is not absolute", s.Path)
	case s.Path != "":
		return []string{"-S", s.Path}, nil
	}
	return []string{"-L", s.Name}, nil
}

// Request is one tmux client invocation: one or more commands run in order by
// the server without returning to its event loop (so e.g. remain-on-exit is
// set before an instantly-exiting child is reaped).
type Request struct {
	Socket   Socket
	Commands [][]string // each command's argv (without "tmux"); joined with ";"
	Stdin    io.Reader  // optional (load-buffer -)
}

// Result is the captured output of an invocation.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// Runner executes tmux invocations. Implementations must reject a zero socket.
type Runner interface {
	Run(ctx context.Context, req Request) (Result, error)
}

// Argv builds the client argument vector: socket flag, optional -f config
// (only meaningful when the call starts a server), then each command with
// every argument escaped, separated by ";" arguments.
func Argv(req Request, configFile string) ([]string, error) {
	flags, err := req.Socket.flags()
	if err != nil {
		return nil, err
	}
	if len(req.Commands) == 0 {
		return nil, errors.New("tmux: empty command list")
	}
	argv := append([]string(nil), flags...)
	if configFile != "" {
		argv = append(argv, "-f", configFile)
	}
	for i, cmd := range req.Commands {
		if len(cmd) == 0 {
			return nil, errors.New("tmux: empty command")
		}
		if i > 0 {
			argv = append(argv, ";")
		}
		for _, a := range cmd {
			argv = append(argv, EscapeArg(a))
		}
	}
	return argv, nil
}

// EscapeArg protects an argument from tmux's command-line parser, which ends
// a command at any argument with a trailing ';' (and turns a trailing `\;`
// into ';'). Inserting a backslash before a trailing ';' makes tmux deliver
// the original text. Nothing else in an argument is special to the parser.
func EscapeArg(s string) string {
	if strings.HasSuffix(s, ";") {
		return s[:len(s)-1] + `\;`
	}
	return s
}

// EscapeFormat doubles '#' for arguments tmux format-expands (e.g. -c start
// directories), so "#(cmd)" or "#{var}" in a path is taken literally and never
// runs a job.
func EscapeFormat(s string) string { return strings.ReplaceAll(s, "#", "##") }

// ExecRunner runs the real tmux binary.
type ExecRunner struct {
	Bin        string   // tmux binary; default "tmux"
	Env        []string // child environment; nil inherits the process env
	ConfigFile string   // -f for server start (testenv uses /dev/null)
}

// Run executes one invocation. A non-zero exit becomes *Error with stderr.
func (r ExecRunner) Run(ctx context.Context, req Request) (Result, error) {
	argv, err := Argv(req, r.ConfigFile)
	if err != nil {
		return Result{}, err
	}
	bin := r.Bin
	if bin == "" {
		bin = "tmux"
	}
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Env = r.Env
	cmd.Stdin = req.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if runErr != nil {
		return res, newError(commandNames(req.Commands), stderr.String(), runErr)
	}
	return res, nil
}

func commandNames(cmds [][]string) []string {
	names := make([]string, 0, len(cmds))
	for _, c := range cmds {
		if len(c) > 0 {
			names = append(names, c[0])
		}
	}
	return names
}

// Error is a failed tmux invocation.
type Error struct {
	Commands []string // command names, e.g. [new-session set-option]
	Stderr   string   // first non-empty stderr line
	Err      error
}

func newError(cmds []string, stderr string, err error) *Error {
	return &Error{Commands: cmds, Stderr: firstLine(stderr), Err: err}
}

func (e *Error) Error() string {
	msg := e.Stderr
	if msg == "" {
		msg = e.Err.Error()
	}
	return "tmux " + strings.Join(e.Commands, "+") + ": " + msg
}

// Unwrap exposes ErrNoServer for a missing/dead server and the exec error.
func (e *Error) Unwrap() []error {
	errs := []error{e.Err}
	if isNoServer(e.Stderr) {
		errs = append(errs, ErrNoServer)
	}
	return errs
}

func isNoServer(stderr string) bool {
	return strings.HasPrefix(stderr, "no server running on") || strings.HasPrefix(stderr, "error connecting to")
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// Client binds a Runner to one socket.
type Client struct {
	Runner Runner
	Socket Socket
}

// NewClient returns a client for socket.
func NewClient(r Runner, s Socket) Client { return Client{Runner: r, Socket: s} }

// run executes cmds as one invocation.
func (c Client) run(ctx context.Context, stdin io.Reader, cmds ...[]string) (Result, error) {
	if c.Runner == nil {
		return Result{}, errors.New("tmux: client has no runner")
	}
	return c.Runner.Run(ctx, Request{Socket: c.Socket, Commands: cmds, Stdin: stdin})
}
