// Package claude is the Claude Code AgentAdapter. One file per interface
// method: start.go (Start, owned by the skeleton), settings.go (hook settings
// generation) and hook.go (ParseHook) by m1-hook-state, observe.go and
// stop.go by m1-pane-identity, deliver.go by m2-prompt-queue.
package claude

import (
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Name is the adapter / agent_type name.
const Name = "claude"

// SessionPrefix prefixes every launched tmux session name.
const SessionPrefix = "jpark-"

// Config wires the adapter's dependencies.
type Config struct {
	Runner       tmux.Runner      // runs tmux; every call names a socket explicitly
	AgentctlPath string           // absolute path of the agentctl binary the hook command invokes
	Now          func() time.Time // default time.Now
}

// Adapter implements adapter.AgentAdapter for Claude Code.
type Adapter struct {
	cfg Config
}

var _ adapter.AgentAdapter = (*Adapter)(nil)

// New returns a Claude Code adapter.
func New(cfg Config) *Adapter {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Adapter{cfg: cfg}
}

// Name implements adapter.AgentAdapter.
func (a *Adapter) Name() string { return Name }

// SessionName is the tmux session name for a launched task.
func SessionName(taskID string) string { return SessionPrefix + taskID }

// SettingsPath is the generated hook settings file passed to --settings.
func SettingsPath(home string) string { return paths.ForHome(home).ClaudeSettings }

// LaunchArgv is the agent argv: prefix + --settings <file> [goal]. The goal is
// one element (never shell-parsed) and is omitted when empty.
func LaunchArgv(prefix []string, settingsFile, goal string) []string {
	argv := append(append([]string(nil), prefix...), "--settings", settingsFile)
	if goal != "" {
		argv = append(argv, goal)
	}
	return argv
}

// client binds the adapter's runner to a socket path.
func (a *Adapter) client(socket string) tmux.Client {
	return tmux.NewClient(a.cfg.Runner, tmux.Socket{Path: socket})
}
