// Package codex is the Codex CLI AgentAdapter (codex-support design,
// docs/superpowers/specs/2026-11-12-codex-support-design.md).
//
// Codex is only tracked in embedded mode: agentctl passes its hooks on the
// command line with -c (hook flags), which also makes Codex run its session
// inside the pane's own process. There the hook processes inherit the pane's
// $TMUX/$TMUX_PANE. A Codex started normally runs sessions in the shared
// `codex app-server` daemon, whose hooks report the daemon's pane, not the
// user's; agentctl never trusts those (hookingest's daemon guard).
//
// Pane-level operations (Observe, Deliver, Stop) are agent-independent tmux
// work and are delegated to the Claude adapter, which owns the
// identity-checked paste and kill paths (archtest rules paste-sendkeys and
// kill-commands).
package codex

import (
	"context"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Name is the adapter / agent_type name.
const Name = "codex"

// Config wires the adapter's dependencies.
type Config struct {
	Runner       tmux.Runner      // runs tmux; every call names a socket explicitly
	AgentctlPath string           // absolute path of the agentctl binary the hook command invokes
	Now          func() time.Time // default time.Now
}

// Adapter implements adapter.AgentAdapter for Codex CLI.
type Adapter struct {
	cfg  Config
	pane *claude.Adapter // Observe, Deliver, Stop: agent-independent pane work
}

var _ adapter.AgentAdapter = (*Adapter)(nil)

// New returns a Codex adapter.
func New(cfg Config) *Adapter {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Adapter{cfg: cfg, pane: claude.New(claude.Config{Runner: cfg.Runner, AgentctlPath: cfg.AgentctlPath, Now: cfg.Now})}
}

// Name implements adapter.AgentAdapter.
func (a *Adapter) Name() string { return Name }

// Observe implements adapter.AgentAdapter (pane liveness; see claude.Observe).
func (a *Adapter) Observe(ctx context.Context, h adapter.Handle) (adapter.Observation, error) {
	return a.pane.Observe(ctx, h)
}

// Deliver implements adapter.AgentAdapter: the same identity-checked
// bracketed paste + Enter as Claude (Codex's composer accepts bracketed paste).
func (a *Adapter) Deliver(ctx context.Context, h adapter.Handle, m adapter.Message) (adapter.Receipt, error) {
	return a.pane.Deliver(ctx, h, m)
}

// Stop implements adapter.AgentAdapter (verified kill; see claude.Stop).
func (a *Adapter) Stop(ctx context.Context, h adapter.Handle) error {
	return a.pane.Stop(ctx, h)
}
