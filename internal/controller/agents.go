package controller

import (
	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/adapter/codex"
)

// EnvCodexCmd is a JSON array giving the Codex argv prefix (default ["codex"]).
const EnvCodexCmd = "AGENTCTL_CODEX_CMD"

// DefaultCodexCmd is used when EnvCodexCmd is unset.
var DefaultCodexCmd = []string{"codex"}

// agentFor returns the adapter for an agent type ("" means claude). Every
// per-session operation picks it by the session's agent_type; launches pick
// it by the request.
func (c *Controller) agentFor(agentType string) (adapter.AgentAdapter, error) {
	if agentType == "" {
		agentType = claude.Name
	}
	if a, ok := c.agents[agentType]; ok {
		return a, nil
	}
	return nil, userErr(ErrInvalidAgent, nil, "unknown agent %q (use claude or codex)", agentType)
}

// agentCmdEnv is the env var and default argv prefix of each agent.
func agentCmdEnv(agentType string) (envVar string, def []string, hint string) {
	if agentType == codex.Name {
		return EnvCodexCmd, DefaultCodexCmd, "install Codex CLI or set " + EnvCodexCmd
	}
	return EnvAgentCmd, DefaultAgentCmd, "install Claude Code or set " + EnvAgentCmd
}
