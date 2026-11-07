package domain

import "strconv"

// Identity binds a session to exactly one tmux pane. It is captured at launch
// or adoption and never inferred from cwd or command name (invariant 5).
type Identity struct {
	Socket    string // absolute, symlink-resolved tmux socket path
	ServerPID int    // tmux server pid at capture time
	SessionID string // tmux session id, e.g. "$3"
	PaneID    string // tmux pane id, e.g. "%7" (reused across server restarts!)
	PanePID   int    // pid of the pane's process at capture time
}

// IsZero reports whether no identity was captured (e.g. a failed launch).
func (id Identity) IsZero() bool { return id.PaneID == "" }

// PaneObservation is what one tmux probe saw for an identity's socket.
type PaneObservation struct {
	ServerReachable bool // false: socket missing or no server answering
	ServerPID       int  // the answering server's pid
	Found           bool // a pane with Identity.PaneID exists on that server
	PanePID         int  // that pane's pane_pid
	Dead            bool // pane_dead (remain-on-exit keeps it around)
	DeadStatus      int  // pane_dead_status
	DeadSignal      int  // pane_dead_signal (0 if not killed by a signal)
}

// LivenessStatus is the coarse result of identity verification.
type LivenessStatus string

const (
	LivenessAlive        LivenessStatus = "alive"        // identity matches and the process runs
	LivenessDead         LivenessStatus = "dead"         // identity matches; the process exited
	LivenessDisconnected LivenessStatus = "disconnected" // identity cannot be verified any more
	LivenessUnverified   LivenessStatus = "unverified"   // probe failed transiently; conclude nothing
)

// DisconnectReason says why identity verification failed.
type DisconnectReason string

const (
	ReasonPaneMissing      DisconnectReason = "pane_missing"      // server is ours, pane id is gone
	ReasonServerGone       DisconnectReason = "server_gone"       // socket missing / no server answering
	ReasonIdentityMismatch DisconnectReason = "identity_mismatch" // server pid or pane pid differs (pane-id reuse)
	ReasonNoIdentity       DisconnectReason = "no_identity"       // nothing was ever captured
)

// Liveness is the verdict of ClassifyLiveness.
type Liveness struct {
	Status     LivenessStatus
	Reason     DisconnectReason // set when Status == LivenessDisconnected
	ExitStatus int              // set when Status == LivenessDead
	Signal     int              // set when Status == LivenessDead and killed by a signal
	Detail     string           // set when Status == LivenessUnverified
}

// UnverifiedLiveness reports a probe that could not run (e.g. tmux exec
// failure, context cancelled). It never implies a state change, because
// disconnected is sticky and a transient error must not make it so.
func UnverifiedLiveness(detail string) Liveness {
	return Liveness{Status: LivenessUnverified, Detail: detail}
}

// IsAlive reports whether the pane is verified to be ours and running.
// Capture, attach, and delivery require this (dead panes may still be
// captured; callers that allow that check Status == LivenessDead too).
func (l Liveness) IsAlive() bool { return l.Status == LivenessAlive }

// IsVerified reports whether the pane is verified to be ours (alive or dead).
func (l Liveness) IsVerified() bool {
	return l.Status == LivenessAlive || l.Status == LivenessDead
}

// ImpliedState maps a verdict to the agent state reconcile should record:
// dead 0 -> stopped, dead non-zero or by signal -> failed, disconnected ->
// disconnected. ok is false for alive and unverified (no change implied).
func (l Liveness) ImpliedState() (state AgentState, detail string, ok bool) {
	switch l.Status {
	case LivenessDead:
		if l.Signal != 0 {
			return StateFailed, "signal " + strconv.Itoa(l.Signal), true
		}
		if l.ExitStatus != 0 {
			return StateFailed, "exit " + strconv.Itoa(l.ExitStatus), true
		}
		return StateStopped, "exit 0", true
	case LivenessDisconnected:
		return StateDisconnected, "disconnected: " + string(l.Reason), true
	}
	return "", "", false
}

// String renders the verdict for one-line messages.
func (l Liveness) String() string {
	switch l.Status {
	case LivenessDisconnected:
		return "disconnected: " + string(l.Reason)
	case LivenessDead:
		_, d, _ := l.ImpliedState()
		return "dead (" + d + ")"
	case LivenessUnverified:
		return "unverified: " + l.Detail
	}
	return string(l.Status)
}

// ClassifyLiveness compares a stored identity with a fresh observation. The
// order matters: a different server pid means the server we bound to is gone
// and %N may have been reused, so it is a mismatch even when a pane with the
// same id exists.
func ClassifyLiveness(id Identity, obs PaneObservation) Liveness {
	if id.IsZero() {
		return Liveness{Status: LivenessDisconnected, Reason: ReasonNoIdentity}
	}
	if !obs.ServerReachable {
		return Liveness{Status: LivenessDisconnected, Reason: ReasonServerGone}
	}
	if obs.ServerPID != id.ServerPID {
		return Liveness{Status: LivenessDisconnected, Reason: ReasonIdentityMismatch}
	}
	if !obs.Found {
		return Liveness{Status: LivenessDisconnected, Reason: ReasonPaneMissing}
	}
	if obs.PanePID != id.PanePID {
		return Liveness{Status: LivenessDisconnected, Reason: ReasonIdentityMismatch}
	}
	if obs.Dead {
		return Liveness{Status: LivenessDead, ExitStatus: obs.DeadStatus, Signal: obs.DeadSignal}
	}
	return Liveness{Status: LivenessAlive}
}
