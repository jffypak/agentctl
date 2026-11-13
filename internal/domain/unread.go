package domain

import "time"

// UnreadInput is what decides whether a task has a result the user has not
// seen yet.
type UnreadInput struct {
	TaskState   TaskState
	AgentState  AgentState
	TurnEndedAt time.Time // when the agent last finished a turn (zero = never)
	ViewedAt    time.Time // when the user last looked at the task (zero = never)
}

// IsUnread reports a finished turn the user has not looked at since: the
// last turn ended after the last view, the task is open, and the agent is
// not already working on the next turn (whose result will supersede it).
func IsUnread(in UnreadInput) bool {
	switch {
	case in.TaskState != TaskRunning, in.TurnEndedAt.IsZero(), in.AgentState == StateWorking:
		return false
	}
	return in.TurnEndedAt.After(in.ViewedAt)
}
