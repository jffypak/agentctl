package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jffypak/agentctl/internal/adapter/claude"
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
	_, err := c.requireTools(claude.Name)
	return err
}

func (c *Controller) requireTools(agentType string) (tools, error) {
	argv, err := c.agentCommand(agentType)
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
		_, _, hint := agentCmdEnv(agentType)
		return tools{}, userErr(ErrAgentMissing, err, "agent command %q not found on PATH (%s)", argv[0], hint)
	}
	t.agentCmd = append([]string{resolved}, argv[1:]...)
	return t, nil
}

// agentCommand parses the agent's command env var (EnvAgentCmd for Claude,
// EnvCodexCmd for Codex).
func (c *Controller) agentCommand(agentType string) ([]string, error) {
	envVar, def, _ := agentCmdEnv(agentType)
	raw := strings.TrimSpace(c.Getenv(envVar))
	if raw == "" {
		return append([]string(nil), def...), nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil || len(argv) == 0 || argv[0] == "" {
		return nil, userErr(ErrAgentCmdInvalid, err, `%s must be a non-empty JSON array of strings, e.g. ["%s"]`, envVar, def[0])
	}
	for _, a := range argv {
		if strings.IndexByte(a, 0) >= 0 {
			return nil, userErr(ErrAgentCmdInvalid, nil, "%s contains a NUL byte", envVar)
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
