// Package controller holds agentctl's use cases. With hookingest it is the
// only writer of task / session / message state; the TUI and the CLI act
// through it. One file per use case: newtask.go, overview.go, live.go
// (identity verification) here; reconcile.go, adopt.go, close.go
// (m1-pane-identity); inspect.go (m1-inspect); attach.go, uievents.go,
// stats.go (m2-tui-inbox); send.go, drain.go, messages.go, deliver.go
// (m2-prompt-queue).
package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/gitx"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Config wires a Controller. Only Environ is commonly set; every other field
// has a production default.
type Config struct {
	// Environ is the environment used for every lookup (AGENTCTL_*, PATH,
	// TMUX, XDG_*) and handed to git/tmux children. nil means os.Environ().
	// Tests pass testenv.Env so nothing reads the real process environment.
	Environ []string
	// Paths overrides home resolution (zero value: paths.Resolve(Environ)).
	Paths paths.Paths
	// Tunables overrides timing (zero value: DefaultTunables()).
	Tunables Tunables
	// Now is the clock (default time.Now). TUI goldens inject a fixed clock.
	Now func() time.Time
	// TmuxRunner replaces the exec runner (TUI tests use a recording runner).
	TmuxRunner tmux.Runner
	// AgentctlPath is the agentctl binary hooks invoke (default os.Executable()).
	AgentctlPath string
	// BusyTimeout bounds every registry write's wait for the SQLite write
	// lock (default store.DefaultBusyTimeout). Tests shorten it.
	BusyTimeout time.Duration
}

// Controller is safe for concurrent use.
type Controller struct {
	env          []string
	paths        paths.Paths
	tun          Tunables
	now          func() time.Time
	store        *store.Store
	tmuxRunner   tmux.Runner
	tmuxInjected bool
	git          gitx.Git
	agents       map[string]adapter.AgentAdapter // by agent_type (agents.go)
	procs        procLister                      // process table for adopt matching (adopt_match.go)
	uid          int
}

// Open resolves paths, creates the home directory (0700), opens the registry,
// and wires git, tmux and the Claude adapter.
func Open(ctx context.Context, cfg Config) (*Controller, error) {
	env := cfg.Environ
	if env == nil {
		env = os.Environ()
	}
	c := &Controller{env: append([]string(nil), env...), tun: cfg.Tunables.withDefaults(), now: cfg.Now, uid: os.Getuid()}
	if c.now == nil {
		c.now = time.Now
	}
	c.paths = cfg.Paths
	if c.paths.Home == "" {
		p, err := paths.Resolve(c.Getenv)
		if err != nil {
			return nil, err
		}
		c.paths = p
	}
	if err := paths.EnsureHome(c.paths); err != nil {
		return nil, fmt.Errorf("cannot create agentctl home: %w", err)
	}
	st, err := store.Open(ctx, c.paths.DB, store.Options{Now: c.now, BusyTimeout: cfg.BusyTimeout})
	if err != nil {
		return nil, fmt.Errorf("cannot open registry %s: %w", c.paths.DB, err)
	}
	c.store = st

	gitBin, err := c.lookPath("git")
	if err != nil {
		gitBin = "git" // preflight reports ErrGitMissing when a command needs git
	}
	c.git = gitx.Git{Bin: gitBin, Env: c.env}
	if cfg.TmuxRunner != nil {
		c.tmuxRunner, c.tmuxInjected = cfg.TmuxRunner, true
	} else {
		tmuxBin, err := c.lookPath("tmux")
		if err != nil {
			tmuxBin = "tmux"
		}
		c.tmuxRunner = tmux.ExecRunner{Bin: tmuxBin, Env: c.env}
	}
	exe := cfg.AgentctlPath
	if exe == "" {
		exe, _ = os.Executable()
	}
	c.procs = c.psProcs
	c.agents = map[string]adapter.AgentAdapter{
		claude.Name: claude.New(claude.Config{Runner: c.tmuxRunner, AgentctlPath: exe, Now: c.now}),
		codex.Name:  codex.New(codex.Config{Runner: c.tmuxRunner, AgentctlPath: exe, Now: c.now}),
	}
	return c, nil
}

// Close closes the registry.
func (c *Controller) Close() error { return c.store.Close() }

// Paths is the resolved home layout.
func (c *Controller) Paths() paths.Paths { return c.paths }

// Tunables are the effective timing knobs.
func (c *Controller) Tunables() Tunables { return c.tun }

// Now is the controller clock.
func (c *Controller) Now() time.Time { return c.now() }

// Getenv looks a variable up in the controller's environment (last wins).
func (c *Controller) Getenv(key string) string {
	for i := len(c.env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(c.env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// tmuxClient binds the runner to a socket path.
func (c *Controller) tmuxClient(socket string) tmux.Client {
	return tmux.NewClient(c.tmuxRunner, tmux.Socket{Path: socket})
}
