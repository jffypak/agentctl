// Package domain holds agentctl's pure types: state enums, transition tables,
// attention and liveness classification, slug rules, terminal-text
// sanitization, and event-type constants.
//
// Invariant 10: this package performs no I/O. archtest enforces an import
// allowlist of pure stdlib packages.
package domain

// AgentState is the observed state of the agent running in a session.
type AgentState string

const (
	StateStarting      AgentState = "starting"       // launched, no hook seen yet
	StateReady         AgentState = "ready"          // turn finished; deliverable when confidence is hook
	StateWorking       AgentState = "working"        // mid-turn
	StateAwaitingInput AgentState = "awaiting_input" // permission prompt / question; never paste here
	StateUnknown       AgentState = "unknown"        // no reliable evidence (adopted without hooks, starting timeout)
	StateStopped       AgentState = "stopped"        // exited cleanly (SessionEnd or dead pane, status 0); sticky
	StateFailed        AgentState = "failed"         // exited non-zero / launch failed; sticky
	StateDisconnected  AgentState = "disconnected"   // identity no longer verifiable; sticky
)

// AgentStates lists every agent state in a stable order.
func AgentStates() []AgentState {
	return []AgentState{
		StateStarting, StateReady, StateWorking, StateAwaitingInput,
		StateUnknown, StateStopped, StateFailed, StateDisconnected,
	}
}

// Valid reports whether s is a known agent state.
func (s AgentState) Valid() bool {
	switch s {
	case StateStarting, StateReady, StateWorking, StateAwaitingInput,
		StateUnknown, StateStopped, StateFailed, StateDisconnected:
		return true
	}
	return false
}

// IsTerminal reports whether s is sticky: no later observation moves a
// session out of it.
func (s AgentState) IsTerminal() bool {
	return s == StateStopped || s == StateFailed || s == StateDisconnected
}

// Confidence records where the current agent state came from.
type Confidence string

const (
	ConfidenceHook Confidence = "hook" // from a Claude Code lifecycle hook
	ConfidencePane Confidence = "pane" // from tmux pane observation (reconcile)
	ConfidenceNone Confidence = "none" // no evidence (starting, unknown)
)

// Valid reports whether c is a known confidence level.
func (c Confidence) Valid() bool {
	return c == ConfidenceHook || c == ConfidencePane || c == ConfidenceNone
}

// TaskState is the lifecycle of a task row. Closing never touches the
// session or the worktree.
type TaskState string

const (
	TaskRunning TaskState = "running"
	TaskClosed  TaskState = "closed"
)

// Valid reports whether s is a known task state.
func (s TaskState) Valid() bool { return s == TaskRunning || s == TaskClosed }

// Origin records how a session came under management.
type Origin string

const (
	OriginLaunched Origin = "launched" // started by `agentctl new`
	OriginAdopted  Origin = "adopted"  // registered by `agentctl adopt`
	OriginWrapped  Origin = "wrapped"  // registered by `agentctl codex` in the user's own pane
)

// Valid reports whether o is a known origin.
func (o Origin) Valid() bool {
	return o == OriginLaunched || o == OriginAdopted || o == OriginWrapped
}

// MessageState is the delivery lifecycle of a queued prompt.
//
//	queued -> delivering -> sent -> acknowledged
//	                     \-> unconfirmed (-> acknowledged on a late hash match)
//	queued -> cancelled | failed
type MessageState string

const (
	MsgQueued       MessageState = "queued"
	MsgDelivering   MessageState = "delivering"
	MsgSent         MessageState = "sent"
	MsgAcknowledged MessageState = "acknowledged"
	MsgUnconfirmed  MessageState = "unconfirmed"
	MsgFailed       MessageState = "failed"
	MsgCancelled    MessageState = "cancelled"
)

// MessageStates lists every message state in a stable order.
func MessageStates() []MessageState {
	return []MessageState{
		MsgQueued, MsgDelivering, MsgSent, MsgAcknowledged,
		MsgUnconfirmed, MsgFailed, MsgCancelled,
	}
}

// Valid reports whether s is a known message state.
func (s MessageState) Valid() bool {
	switch s {
	case MsgQueued, MsgDelivering, MsgSent, MsgAcknowledged,
		MsgUnconfirmed, MsgFailed, MsgCancelled:
		return true
	}
	return false
}

// AckMatch records how a sent message was acknowledged.
type AckMatch string

const (
	AckHash   AckMatch = "hash"   // UserPromptSubmit prompt hash equals body hash
	AckTiming AckMatch = "timing" // first submit within AckWindow of sent
)

// Valid reports whether a is a known ack match kind.
func (a AckMatch) Valid() bool { return a == AckHash || a == AckTiming }

// Transition sources recorded in agent.state_changed payloads.
const (
	SourceHook     = "hook"      // agentctl hook
	SourcePane     = "pane"      // reconcile observed the pane
	SourceTimeout  = "timeout"   // StartingTimeout elapsed
	SourcePaneText = "pane_text" // reconcile read the pane's screen (folder-trust dialog before the first hook)
	SourceLaunch   = "launch"    // agentctl new
	SourceUser     = "user"      // explicit user action (close --stop)
)
