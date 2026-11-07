package claude

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Bounds on hook-supplied text kept in an observation (bytes, after sanitizing).
const (
	MaxHookDetail = 200 // HookObservation.Detail
	maxAttrLen    = 64  // each HookObservation.Attrs value
	maxSessionID  = 128 // HookObservation.AgentSessionID
	maxEventName  = 64  // hook_event_name
)

// hookPayload is the subset of a Claude Code 2.1.x hook payload that agentctl
// reads (field names from the hooks reference; see docs/notes/m1-hook-state.md).
// It deliberately has no transcript_path, tool_input, tool_response or
// last_assistant_message field: those are never decoded, kept, or opened.
// prompt is decoded only to be hashed.
type hookPayload struct {
	SessionID        string `json:"session_id"`
	Cwd              string `json:"cwd"`
	HookEventName    string `json:"hook_event_name"`
	Source           string `json:"source"`            // SessionStart
	Prompt           string `json:"prompt"`            // UserPromptSubmit: hashed, then dropped
	ToolName         string `json:"tool_name"`         // PreToolUse, PostToolUse, PermissionRequest
	NotificationType string `json:"notification_type"` // Notification
	Message          string `json:"message"`           // Notification
	Trigger          string `json:"trigger"`           // PreCompact
	Reason           string `json:"reason"`            // SessionEnd
	Error            string `json:"error"`             // StopFailure (recorded only)
}

// ParseHook maps one Claude Code hook payload (stdin JSON) to an observation,
// following the hook->state table in ARCHITECTURE.md §Agent adapter.
// HookObservation.State == "" means "record only, no state change". It is
// pure: no I/O, transcript_path is never opened, and of the prompt only its
// sha256 is kept. A payload that is not a single JSON object with a valid
// hook_event_name is an error.
func (a *Adapter) ParseHook(payload []byte, _ adapter.HookEnv) (adapter.HookObservation, error) {
	p, err := decodeHook(payload)
	if err != nil {
		return adapter.HookObservation{}, err
	}
	obs := adapter.HookObservation{
		Event:          p.HookEventName,
		AgentSessionID: bound(hookLine(p.SessionID), maxSessionID),
		Cwd:            p.Cwd,
		Attrs:          map[string]string{},
	}
	for k, v := range map[string]string{
		"source": p.Source, "tool_name": p.ToolName, "notification_type": p.NotificationType,
		"trigger": p.Trigger, "reason": p.Reason, "error": p.Error,
	} {
		if v = bound(hookLine(v), maxAttrLen); v != "" {
			obs.Attrs[k] = v
		}
	}
	obs.State, obs.Detail = mapHook(p, obs.Attrs)
	obs.Detail = bound(hookLine(obs.Detail), MaxHookDetail)
	if p.HookEventName == "UserPromptSubmit" && p.Prompt != "" {
		sum := sha256.Sum256([]byte(p.Prompt))
		obs.PromptSHA256 = hex.EncodeToString(sum[:])
	}
	return obs, nil
}

func decodeHook(payload []byte) (hookPayload, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return hookPayload{}, errors.New("claude: empty hook payload")
	}
	if trimmed[0] != '{' {
		return hookPayload{}, errors.New("claude: hook payload is not a JSON object")
	}
	var p hookPayload
	if err := json.Unmarshal(trimmed, &p); err != nil {
		return hookPayload{}, fmt.Errorf("claude: malformed hook payload: %s", hookLine(err.Error()))
	}
	if !validEventName(p.HookEventName) {
		return hookPayload{}, fmt.Errorf("claude: hook payload has no valid hook_event_name (%q)", bound(hookLine(p.HookEventName), maxEventName))
	}
	return p, nil
}

func validEventName(s string) bool {
	if s == "" || len(s) > maxEventName {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// mapHook is the hook->state table. attrs are the already-bounded fields.
func mapHook(p hookPayload, attrs map[string]string) (domain.AgentState, string) {
	switch p.HookEventName {
	case "SessionStart":
		if p.Source == "compact" {
			return "", "" // auto-compact fires mid-turn; ready here could allow a paste into a working agent
		}
		return domain.StateReady, withParen("session started", attrs["source"])
	case "UserPromptSubmit":
		return domain.StateWorking, "prompt submitted"
	case "PreToolUse":
		if p.ToolName == "AskUserQuestion" {
			return domain.StateAwaitingInput, "question for you (AskUserQuestion)"
		}
		return domain.StateWorking, withColon("tool", attrs["tool_name"])
	case "PostToolUse":
		return domain.StateWorking, withColon("tool done", attrs["tool_name"])
	case "PreCompact":
		return domain.StateWorking, withParen("compacting", attrs["trigger"])
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt":
			return domain.StateAwaitingInput, withColon("permission prompt", p.Message)
		case "elicitation_dialog":
			return domain.StateAwaitingInput, withColon("input requested", p.Message)
		case "idle_prompt":
			return domain.StateReady, withColon("idle", p.Message)
		}
		return "", ""
	case "PermissionRequest":
		return domain.StateAwaitingInput, withColon("permission request", attrs["tool_name"])
	case "Stop":
		return domain.StateReady, "turn finished"
	case "SessionEnd":
		return domain.StateStopped, withParen("session ended", attrs["reason"])
	}
	return "", ""
}

func withColon(label, v string) string {
	if v = strings.TrimSpace(v); v == "" {
		return label
	}
	return label + ": " + v
}

func withParen(label, v string) string {
	if v == "" {
		return label
	}
	return label + " (" + v + ")"
}

// hookLine sanitizes terminal control sequences and folds whitespace runs
// (including newlines and tabs) into single spaces.
func hookLine(s string) string {
	return strings.Join(strings.Fields(domain.SanitizeTerminalText(s)), " ")
}

// bound truncates s to at most n bytes on a rune boundary.
func bound(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// HookPane is the tmux pane a hook process runs in, read from the $TMUX and
// $TMUX_PANE it inherits from the agent pane.
type HookPane struct {
	Socket    string // as given in $TMUX (not canonicalized)
	ServerPID int
	PaneID    string // "%N"
}

// PaneFromEnv parses $TMUX ("<socket>,<server pid>,<session index>"; the
// socket may itself contain commas) and $TMUX_PANE ("%N"). Pure.
func PaneFromEnv(env adapter.HookEnv) (HookPane, error) {
	if env.TMUX == "" || env.TMUXPane == "" {
		return HookPane{}, errors.New("claude: hook is not running inside tmux ($TMUX/$TMUX_PANE unset)")
	}
	sock := tmux.SocketFromTMUX(env.TMUX)
	if sock == "" || !filepath.IsAbs(sock) {
		return HookPane{}, fmt.Errorf("claude: cannot parse $TMUX %q", bound(hookLine(env.TMUX), 256))
	}
	rest := strings.TrimPrefix(env.TMUX, sock+",")
	pidStr, _, _ := strings.Cut(rest, ",")
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return HookPane{}, fmt.Errorf("claude: cannot parse the server pid in $TMUX %q", bound(hookLine(env.TMUX), 256))
	}
	if !validPaneID(env.TMUXPane) {
		return HookPane{}, fmt.Errorf("claude: invalid $TMUX_PANE %q", bound(hookLine(env.TMUXPane), 32))
	}
	return HookPane{Socket: sock, ServerPID: pid, PaneID: env.TMUXPane}, nil
}

func validPaneID(s string) bool {
	if len(s) < 2 || s[0] != '%' {
		return false
	}
	_, err := strconv.ParseUint(s[1:], 10, 32)
	return err == nil
}

// Matches reports whether the hook's pane is the session's pane: same pane
// id, same server pid (a restarted server reuses %N), and the same socket
// after canonicalization (symlinks resolved, so it touches the filesystem).
// reason names the first mismatch.
func (p HookPane) Matches(id domain.Identity) (ok bool, reason string) {
	switch {
	case id.IsZero():
		return false, "session_has_no_identity"
	case p.PaneID != id.PaneID:
		return false, "pane_mismatch"
	case p.ServerPID != id.ServerPID:
		return false, "server_pid_mismatch"
	case !tmux.SameSocket(p.Socket, id.Socket):
		return false, "socket_mismatch"
	}
	return true, ""
}
