package domain

// Event types appended to the events table. Payload shapes are documented in
// docs/notes/m1-walking-skeleton.md.
const (
	EventUIOpened = "ui.opened" // {ui_run_id}
	EventUIClosed = "ui.closed" // {ui_run_id}
	EventUIAction = "ui.action" // {ui_run_id, action, task_id}

	EventTaskCreated = "task.created" // {repo, branch, worktree, base_ref, base_sha, origin}
	EventTaskClosed  = "task.closed"  // {}

	EventSessionStarted      = "session.started"       // {socket, server_pid, session_id, pane_id, pane_pid, argv0}
	EventSessionAdopted      = "session.adopted"       // same identity fields as session.started
	EventSessionWrapped      = "session.wrapped"       // agentctl codex registered its own pane; same identity fields
	EventSessionLaunchFailed = "session.launch_failed" // {error}
	EventSessionDisconnected = "session.disconnected"  // {reason}

	EventAgentStateChanged = "agent.state_changed" // {from, to, source, confidence, detail}

	EventMessageQueued       = "message.queued"
	EventMessageClaimed      = "message.claimed"
	EventMessageSent         = "message.sent"
	EventMessageAcknowledged = "message.acknowledged"
	EventMessageUnconfirmed  = "message.unconfirmed"
	EventMessageCancelled    = "message.cancelled"
	EventMessageFailed       = "message.failed"

	EventHookReceived = "hook.received" // {event}
	EventHookOrphan   = "hook.orphan"   // {event, reason}
)

// EventTypes lists every event type constant.
func EventTypes() []string {
	return []string{
		EventUIOpened, EventUIClosed, EventUIAction,
		EventTaskCreated, EventTaskClosed,
		EventSessionStarted, EventSessionAdopted, EventSessionWrapped, EventSessionLaunchFailed, EventSessionDisconnected,
		EventAgentStateChanged,
		EventMessageQueued, EventMessageClaimed, EventMessageSent, EventMessageAcknowledged,
		EventMessageUnconfirmed, EventMessageCancelled, EventMessageFailed,
		EventHookReceived, EventHookOrphan,
	}
}
