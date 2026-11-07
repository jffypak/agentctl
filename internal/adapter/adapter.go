// Package adapter defines the AgentAdapter boundary between agentctl and a
// coding-agent CLI. Phase 1 has one implementation, adapter/claude. Adapters
// report observations; they never mark tasks verified or approved.
package adapter

import (
	"context"
	"errors"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
)

// Sentinel errors.
var (
	// ErrNotReady: Deliver refused because the agent is not deliverable now
	// (not ready, not hook-confident, identity mismatch, or dead pane).
	ErrNotReady = errors.New("agent not ready for delivery")
	// ErrNotImplemented is returned by skeleton stubs until their card lands.
	ErrNotImplemented = errors.New("not implemented")
)

// AgentAdapter launches, observes, delivers to, and stops one kind of agent.
type AgentAdapter interface {
	Name() string
	Start(ctx context.Context, spec LaunchSpec) (Handle, error)        // tmux session in spec.Worktree
	Observe(ctx context.Context, h Handle) (Observation, error)        // pane liveness + hook state
	Deliver(ctx context.Context, h Handle, m Message) (Receipt, error) // ErrNotReady unless deliverable
	Stop(ctx context.Context, h Handle) error                          // only on explicit confirmed close
	ParseHook(payload []byte, env HookEnv) (HookObservation, error)    // hook stdin JSON -> observation
}

// LaunchSpec is everything Start needs. All paths are absolute.
type LaunchSpec struct {
	TaskID   string
	Goal     string   // passed as one argv element, never through a shell
	Worktree string   // session start directory
	Home     string   // AGENTCTL_HOME exported into the session
	Socket   string   // tmux socket to launch on (canonical)
	AgentCmd []string // argv prefix (AGENTCTL_AGENT_CMD); argv[0] resolved to an absolute path
}

// Handle identifies a running agent by explicit pane identity.
type Handle struct {
	Identity    domain.Identity
	SessionName string
	Argv        []string // the full argv launched (informational; never re-run)
}

// Observation is what Observe saw.
type Observation struct {
	Liveness domain.Liveness
	Pane     domain.PaneObservation
}

// Message is a queued prompt to deliver.
type Message struct {
	ID         int64
	Body       string
	BodySHA256 string
}

// Receipt records a completed paste.
type Receipt struct {
	SentAt time.Time
	Buffer string // tmux buffer name used (agentctl-msg-<id>)
}

// HookEnv is the environment a hook process inherits from the agent pane.
type HookEnv struct {
	TaskID   string // AGENTCTL_TASK_ID ("" for adopted panes using hooks-snippet)
	Home     string // AGENTCTL_HOME
	TMUX     string // $TMUX: "<socket>,<server pid>,<session index>"
	TMUXPane string // $TMUX_PANE: "%N"
}

// HookObservation is the adapter's reading of one hook payload.
type HookObservation struct {
	Event          string            // hook_event_name
	AgentSessionID string            // session_id
	Cwd            string            // cwd
	State          domain.AgentState // "" = record only, no state change
	Detail         string            // short human detail; stored sanitized
	PromptSHA256   string            // UserPromptSubmit only; prompt text itself is never kept
	Attrs          map[string]string // extra fields worth recording (notification_type, tool_name, source)
}
