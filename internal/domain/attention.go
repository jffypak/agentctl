package domain

// Section is an attention bucket in `ls` and the TUI.
type Section string

const (
	SectionNeedsYou Section = "needs_you"
	SectionIdle     Section = "idle"
	SectionRunning  Section = "running"
	SectionUnknown  Section = "unknown"
	SectionClosed   Section = "closed" // hidden unless the closed filter is on
)

// Sections lists every section in display order.
func Sections() []Section {
	return []Section{SectionNeedsYou, SectionIdle, SectionRunning, SectionUnknown, SectionClosed}
}

// Order is the display rank of a section (lower first).
func (s Section) Order() int {
	switch s {
	case SectionNeedsYou:
		return 0
	case SectionIdle:
		return 1
	case SectionRunning:
		return 2
	case SectionUnknown:
		return 3
	case SectionClosed:
		return 4
	}
	return 5
}

// Title is the human header for a section.
func (s Section) Title() string {
	switch s {
	case SectionNeedsYou:
		return "NEEDS YOU"
	case SectionIdle:
		return "IDLE"
	case SectionRunning:
		return "RUNNING"
	case SectionUnknown:
		return "UNKNOWN"
	case SectionClosed:
		return "CLOSED"
	}
	return string(s)
}

// MessageSummary counts a task's messages by state.
type MessageSummary struct {
	Queued       int `json:"queued"`
	Delivering   int `json:"delivering"`
	Sent         int `json:"sent"`
	Acknowledged int `json:"acknowledged"`
	Unconfirmed  int `json:"unconfirmed"`
	Failed       int `json:"failed"`
	Cancelled    int `json:"cancelled"`
}

// Add returns a copy of m with n added to the count for state s.
func (m MessageSummary) Add(s MessageState, n int) MessageSummary {
	switch s {
	case MsgQueued:
		m.Queued += n
	case MsgDelivering:
		m.Delivering += n
	case MsgSent:
		m.Sent += n
	case MsgAcknowledged:
		m.Acknowledged += n
	case MsgUnconfirmed:
		m.Unconfirmed += n
	case MsgFailed:
		m.Failed += n
	case MsgCancelled:
		m.Cancelled += n
	}
	return m
}

// AttentionInput is everything attention classification looks at.
type AttentionInput struct {
	TaskState  TaskState
	HasSession bool // false while a task row is reserved but its session is not yet recorded
	AgentState AgentState
	Confidence Confidence
	Messages   MessageSummary
	Origin     Origin // launched agents that fall silent need the user; adopted ones are expected to be unknown
}

// Attention is the classification result: a section plus a short label that
// says why (e.g. "awaiting input", "exited", "queued · attach to deliver").
type Attention struct {
	Section Section
	Label   string
}

// ClassifyAttention buckets a task. Rules (ARCHITECTURE.md §Attention):
//
//   - closed task -> CLOSED (hidden by default)
//   - awaiting_input, failed, disconnected -> NEEDS YOU
//   - any unconfirmed or failed message -> NEEDS YOU
//   - a queued message on an unknown agent -> NEEDS YOU ("attach to deliver")
//   - a queued message on an exited/failed/disconnected agent -> NEEDS YOU
//     (it can never be delivered; honest rather than silently stuck)
//   - ready -> IDLE "ready"; stopped -> IDLE "exited" (never worded as success)
//   - starting, working -> RUNNING
//   - unknown on a *launched* agent -> NEEDS YOU "silent since launch": it only
//     gets there by never sending a hook (trust prompt, login, early hang)
//   - unknown (adopted / no origin), or no session yet -> UNKNOWN
//
// Agent-state reasons outrank message reasons in the label.
func ClassifyAttention(in AttentionInput) Attention {
	if in.TaskState == TaskClosed {
		return Attention{SectionClosed, "closed"}
	}
	if !in.HasSession {
		return Attention{SectionUnknown, "no session"}
	}
	switch in.AgentState {
	case StateAwaitingInput:
		return Attention{SectionNeedsYou, "awaiting input"}
	case StateFailed:
		return Attention{SectionNeedsYou, "failed"}
	case StateDisconnected:
		return Attention{SectionNeedsYou, "disconnected"}
	}
	m := in.Messages
	switch {
	case m.Unconfirmed > 0:
		return Attention{SectionNeedsYou, "message unconfirmed · attach to check"}
	case m.Failed > 0:
		return Attention{SectionNeedsYou, "message failed"}
	case m.Queued > 0 && in.AgentState == StateUnknown:
		return Attention{SectionNeedsYou, "queued · attach to deliver"}
	case m.Queued > 0 && in.AgentState == StateStopped:
		return Attention{SectionNeedsYou, "queued · agent exited"}
	}
	switch in.AgentState {
	case StateReady:
		return Attention{SectionIdle, "ready"}
	case StateStopped:
		return Attention{SectionIdle, "exited"}
	case StateStarting:
		return Attention{SectionRunning, "starting"}
	case StateWorking:
		return Attention{SectionRunning, "working"}
	case StateUnknown:
		if in.Origin == OriginLaunched {
			return Attention{SectionNeedsYou, "silent since launch · attach to check"}
		}
	}
	return Attention{SectionUnknown, "unknown"}
}
