package domain

// agentTransitions is the allowed-move table for agent states. Self moves are
// handled separately (always an allowed no-op). Terminal states have no
// outgoing moves: a later hook or observation never revives them.
var agentTransitions = map[AgentState][]AgentState{
	StateStarting:      {StateReady, StateWorking, StateAwaitingInput, StateUnknown, StateStopped, StateFailed, StateDisconnected},
	StateReady:         {StateWorking, StateAwaitingInput, StateUnknown, StateStopped, StateFailed, StateDisconnected},
	StateWorking:       {StateReady, StateAwaitingInput, StateUnknown, StateStopped, StateFailed, StateDisconnected},
	StateAwaitingInput: {StateReady, StateWorking, StateUnknown, StateStopped, StateFailed, StateDisconnected},
	StateUnknown:       {StateReady, StateWorking, StateAwaitingInput, StateStopped, StateFailed, StateDisconnected},
	StateStopped:       nil,
	StateFailed:        nil,
	StateDisconnected:  nil,
}

// CanTransition reports whether a session may move from one agent state to
// another. from == to is an allowed no-op for valid states (store.Transition
// does not reset state_since or append an event for it). Nothing ever moves
// back to starting. Starting becomes unknown on StartingTimeout; a live
// state becomes unknown only when reconcile finds a lost-hook marker newer
// than it (m2-prompt-queue, ARCHITECTURE.md §Lost-hook safety): the recorded
// state can no longer be trusted. Hooks never produce unknown.
func CanTransition(from, to AgentState) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if from == to {
		return true
	}
	for _, s := range agentTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

var messageTransitions = map[MessageState][]MessageState{
	MsgQueued:       {MsgDelivering, MsgCancelled, MsgFailed},
	MsgDelivering:   {MsgQueued, MsgSent, MsgUnconfirmed, MsgFailed},
	MsgSent:         {MsgAcknowledged, MsgUnconfirmed},
	MsgUnconfirmed:  {MsgAcknowledged},
	MsgAcknowledged: nil,
	MsgFailed:       nil,
	MsgCancelled:    nil,
}

// CanTransitionMessage reports whether a message may move between states.
// Self moves are not allowed: every message write is a real state change made
// by a conditional UPDATE. An unconfirmed message is never re-queued (no
// automatic redelivery); only a late hash match may acknowledge it.
func CanTransitionMessage(from, to MessageState) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	for _, s := range messageTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// CanTransitionTask reports whether a task may move between states. Phase 1
// only closes tasks; reopening is out of scope.
func CanTransitionTask(from, to TaskState) bool {
	return from == TaskRunning && to == TaskClosed
}
