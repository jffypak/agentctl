package domain

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxMessageBytes bounds a prompt body (ARCHITECTURE.md §Send).
const MaxMessageBytes = 64 << 10

// Delivery refusal labels. They are what the user reads next to a queued
// message ("queued · agent working") and in `agentctl send` output.
const (
	LabelWorking      = "agent working"
	LabelAwaiting     = "awaiting input"
	LabelAttach       = "attach to deliver"
	LabelStale        = "state stale — attach to deliver"
	LabelDisconnected = "disconnected"
	LabelExited       = "agent exited"
	LabelFailed       = "agent failed"
	LabelStarting     = "agent starting"
	LabelUnverified   = "pane not verified"
	// LabelNotForeground: a wrapped pane (`agentctl codex`) whose foreground
	// is no longer agentctl codex — Codex exited or the wrapper died — so a
	// paste would land in the user's shell.
	LabelNotForeground = "codex not running in pane · attach to check"
)

// SourceLostHook is the agent.state_changed source of a reconcile downgrade
// caused by a lost-hook marker (a hook whose DB write failed).
const SourceLostHook = "lost_hook"

// DeliveryCheck is everything CanDeliver looks at.
type DeliveryCheck struct {
	State      AgentState
	Confidence Confidence
	Liveness   LivenessStatus // the identity probe at delivery time ("" = not probed: never deliverable)
	StaleHook  bool           // a lost-hook marker is newer than the session's state_since
	// ForegroundMismatch: a wrapped session's pane does not run agentctl
	// codex in its foreground right now (checked only for origin wrapped).
	ForegroundMismatch bool
}

// CanDeliver is the delivery precondition (ARCHITECTURE.md §Delivery rule 2,
// invariant 3): only a ready agent whose state came from a hook, whose pane
// identity verifies alive now, and whose state is not stale may receive a
// paste. Every other combination returns the label saying why not. It
// never allows awaiting_input: text in a permission dialog can approve an
// action.
func CanDeliver(c DeliveryCheck) (ok bool, label string) {
	switch {
	case c.State == StateDisconnected || c.Liveness == LivenessDisconnected:
		return false, LabelDisconnected
	case c.State == StateStopped:
		return false, LabelExited
	case c.State == StateFailed:
		return false, LabelFailed
	case c.Liveness == LivenessDead:
		return false, LabelExited
	case c.ForegroundMismatch:
		return false, LabelNotForeground
	case c.StaleHook:
		return false, LabelStale
	case c.State == StateAwaitingInput:
		return false, LabelAwaiting
	case c.State == StateWorking:
		return false, LabelWorking
	case c.State == StateStarting:
		return false, LabelStarting
	case c.State != StateReady || c.Confidence != ConfidenceHook:
		return false, LabelAttach // unknown, or ready without hook evidence (adopted, pane-only)
	case c.Liveness != LivenessAlive:
		return false, LabelUnverified
	}
	return true, ""
}

// QueueLabel is CanDeliver without the live probe: why a queued message is
// waiting, from stored state alone (TUI rows, `send` output). "" means the
// stored state allows a delivery (the next drain will try).
func QueueLabel(state AgentState, conf Confidence, staleHook bool) string {
	_, label := CanDeliver(DeliveryCheck{State: state, Confidence: conf, Liveness: LivenessAlive, StaleHook: staleHook})
	return label
}

// SendRejected reports whether a send to an agent in state s is refused
// outright (nothing is stored): the agent has exited, failed or is
// disconnected, so the message could never be delivered.
func SendRejected(s AgentState) bool { return s.IsTerminal() }

// MessageError is a body validation failure.
type MessageError struct{ Reason string }

func (e *MessageError) Error() string { return "invalid message: " + e.Reason }

// ValidateMessageBody enforces the Send rules: non-empty (not only
// whitespace), at most MaxMessageBytes, no NUL. It also refuses ESC: the body
// is pasted inside a bracketed paste, and an embedded "ESC[201~" would end
// the paste early and type the rest as keystrokes.
func ValidateMessageBody(body string) error {
	switch {
	case strings.TrimSpace(body) == "":
		return &MessageError{"empty message"}
	case len(body) > MaxMessageBytes:
		return &MessageError{"message is " + strconv.Itoa(len(body)) + " bytes (max " + strconv.Itoa(MaxMessageBytes) + ")"}
	case strings.ContainsRune(body, 0):
		return &MessageError{"message contains a NUL byte"}
	case strings.ContainsRune(body, 0x1b):
		return &MessageError{"message contains an escape character"}
	case !utf8.ValidString(body):
		return &MessageError{"message is not valid UTF-8"}
	}
	return nil
}

// Lost-hook markers (ARCHITECTURE.md §Lost-hook safety). A hook that cannot
// record its event writes <home>/hooks/lost/<key>; every key below that names
// a session blocks delivery to it while the marker is newer than its
// state_since. These are the file names (pure; the I/O lives in hookingest
// and controller).

// LostHookSessionKey names the marker of a resolved session.
func LostHookSessionKey(sessionID int64) string { return "session-" + strconv.FormatInt(sessionID, 10) }

// LostHookTaskKey names the marker of a hook that claimed a task but was not
// resolved to a session ("" for an invalid task id).
func LostHookTaskKey(taskID string) string {
	if ValidateSlug(taskID) != nil {
		return ""
	}
	return "task-" + taskID
}

// LostHookPaneKey names the marker of a hook known only by its $TMUX_PANE
// ("" unless paneID is %N).
func LostHookPaneKey(paneID string) string {
	if len(paneID) < 2 || paneID[0] != '%' {
		return ""
	}
	if _, err := strconv.ParseUint(paneID[1:], 10, 32); err != nil {
		return ""
	}
	return "pane-" + paneID[1:]
}

// LostHookKeys are every marker name that can refer to a session: its own,
// its task's and its pane's. Pane keys are conservative (they ignore the
// socket): a false match only delays a delivery, never causes one.
func LostHookKeys(sessionID int64, taskID, paneID string) []string {
	var out []string
	for _, k := range []string{LostHookSessionKey(sessionID), LostHookTaskKey(taskID), LostHookPaneKey(paneID)} {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}
