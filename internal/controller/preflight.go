package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// EnvAgentCmd is a JSON array giving the agent argv prefix (default ["claude"]).
// Tests substitute e.g. ["sh","-c","exec sleep 600","agent"].
const EnvAgentCmd = "AGENTCTL_AGENT_CMD"

// DefaultAgentCmd is used when EnvAgentCmd is unset.
var DefaultAgentCmd = []string{"claude"}

// tools are the resolved external commands a launch needs.
type tools struct {
	git      string
	tmux     string
	agentCmd []string // argv prefix with argv[0] resolved to an absolute path
}

// Preflight checks that git, tmux and the agent command are available and
// returns the first problem as a one-line *Error (ErrGitMissing,
// ErrTmuxMissing, ErrAgentMissing, ErrAgentCmdInvalid). The TUI uses it for
// error banners.
func (c *Controller) Preflight(ctx context.Context) error {
	_, err := c.requireTools()
	return err
}

func (c *Controller) requireTools() (tools, error) {
	argv, err := c.agentCommand()
	if err != nil {
		return tools{}, err
	}
	var t tools
	if t.git, err = c.lookPath("git"); err != nil {
		return tools{}, userErr(ErrGitMissing, err, "git not found on PATH (install git 2.40 or newer)")
	}
	if !c.tmuxInjected {
		if t.tmux, err = c.lookPath("tmux"); err != nil {
			return tools{}, userErr(ErrTmuxMissing, err, "tmux not found on PATH (install tmux 3.3 or newer)")
		}
	}
	resolved, err := c.lookPath(argv[0])
	if err != nil {
		hint := "install Claude Code or set " + EnvAgentCmd
		return tools{}, userErr(ErrAgentMissing, err, "agent command %q not found on PATH (%s)", argv[0], hint)
	}
	t.agentCmd = append([]string{resolved}, argv[1:]...)
	return t, nil
}

// agentCommand parses EnvAgentCmd.
func (c *Controller) agentCommand() ([]string, error) {
	raw := strings.TrimSpace(c.Getenv(EnvAgentCmd))
	if raw == "" {
		return append([]string(nil), DefaultAgentCmd...), nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil || len(argv) == 0 || argv[0] == "" {
		return nil, userErr(ErrAgentCmdInvalid, err, `%s must be a non-empty JSON array of strings, e.g. ["claude"]`, EnvAgentCmd)
	}
	for _, a := range argv {
		if strings.IndexByte(a, 0) >= 0 {
			return nil, userErr(ErrAgentCmdInvalid, nil, "%s contains a NUL byte", EnvAgentCmd)
		}
	}
	return argv, nil
}

// lookPath resolves file against the controller's PATH (not the process's),
// so hermetic tests can hide tools. Names containing '/' are checked as given.
func (c *Controller) lookPath(file string) (string, error) {
	if strings.Contains(file, "/") {
		abs, err := filepath.Abs(file)
		if err != nil {
			return "", err
		}
		return abs, checkExecutable(abs)
	}
	for _, dir := range filepath.SplitList(c.Getenv("PATH")) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue // never resolve through the current directory
		}
		p := filepath.Join(dir, file)
		if checkExecutable(p) == nil {
			return p, nil
		}
	}
	return "", errors.New(file + ": not found on PATH")
}

func checkExecutable(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
		return errors.New(p + ": not an executable file")
	}
	return nil
}
