package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/domain"
)

// Bounds on hook-supplied text kept in an observation (bytes, after sanitizing).
const (
	maxDetail    = 200
	maxAttrLen   = 64
	maxSessionID = 128
	maxEventName = 64
)

// hookPayload is the subset of a Codex hook payload agentctl reads (field
// names from https://learn.chatgpt.com/docs/hooks and real 0.167.2 payloads).
// transcript_path, tool_input and tool_response are never decoded, kept, or
// opened. prompt and last_assistant_message are kept sanitized and capped
// (adapter.HookText) for the task details.
type hookPayload struct {
	SessionID     string `json:"session_id"`
	Cwd           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
	Source        string `json:"source"`                 // SessionStart: startup|resume|clear|compact|fork
	Prompt        string `json:"prompt"`                 // UserPromptSubmit: hashed, and kept for the details
	LastMessage   string `json:"last_assistant_message"` // Stop
	Model         string `json:"model"`
	ToolName      string `json:"tool_name"` // PreToolUse, PostToolUse, PermissionRequest
	Trigger       string `json:"trigger"`   // PreCompact
	Reason        string `json:"reason"`    // SessionEnd
}

// ParseHook maps one Codex hook payload to an observation (the Codex
// hook->state table in the codex-support design). State "" means record
// only. Notable differences from Claude:
//
//   - SessionStart never sets a state: Codex fires it just before the first
//     prompt's UserPromptSubmit, so ready there would allow a paste into an
//     agent about to work. Stop is the only way to ready.
//   - PermissionRequest is the only approval signal (no Notification).
//   - SessionEnd is detail only, like Claude: the exit is read from the pane
//     (launched) or recorded by the wrapper (`agentctl codex`).
//
// It is pure: no I/O.
func (a *Adapter) ParseHook(payload []byte, _ adapter.HookEnv) (adapter.HookObservation, error) {
	p, err := decodeHook(payload)
	if err != nil {
		return adapter.HookObservation{}, err
	}
	obs := adapter.HookObservation{
		Event:          p.HookEventName,
		AgentSessionID: adapter.Bound(adapter.HookLine(p.SessionID), maxSessionID),
		Cwd:            p.Cwd,
		Attrs:          map[string]string{},
	}
	for k, v := range map[string]string{"source": p.Source, "tool_name": p.ToolName, "trigger": p.Trigger, "reason": p.Reason} {
		if v = adapter.Bound(adapter.HookLine(v), maxAttrLen); v != "" {
			obs.Attrs[k] = v
		}
	}
	obs.State, obs.Detail = mapHook(p.HookEventName, obs.Attrs)
	obs.Detail = adapter.Bound(adapter.HookLine(obs.Detail), maxDetail)
	if p.HookEventName == "UserPromptSubmit" && p.Prompt != "" {
		sum := sha256.Sum256([]byte(p.Prompt))
		obs.PromptSHA256 = hex.EncodeToString(sum[:])
		obs.Prompt = adapter.HookText(p.Prompt)
	}
	if p.HookEventName == "Stop" {
		obs.TurnEnded = true
		obs.LastMessage = adapter.HookText(p.LastMessage)
	}
	obs.Model = adapter.Bound(adapter.HookLine(p.Model), maxAttrLen)
	return obs, nil
}

func mapHook(event string, attrs map[string]string) (domain.AgentState, string) {
	switch event {
	case "SessionStart":
		if attrs["source"] == "compact" {
			return "", ""
		}
		return "", withParen("session started", attrs["source"])
	case "UserPromptSubmit":
		return domain.StateWorking, "prompt submitted"
	case "PreToolUse":
		return domain.StateWorking, withColon("tool", attrs["tool_name"])
	case "PostToolUse":
		return domain.StateWorking, withColon("tool done", attrs["tool_name"])
	case "PreCompact":
		return domain.StateWorking, withParen("compacting", attrs["trigger"])
	case "PermissionRequest":
		return domain.StateAwaitingInput, withColon("permission request", attrs["tool_name"])
	case "Stop":
		return domain.StateReady, "turn finished"
	case "Interrupt":
		return domain.StateReady, "interrupted"
	case "SessionEnd":
		return "", withParen("session ended", attrs["reason"])
	}
	return "", ""
}

func decodeHook(payload []byte) (hookPayload, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return hookPayload{}, errors.New("codex: empty hook payload")
	}
	if trimmed[0] != '{' {
		return hookPayload{}, errors.New("codex: hook payload is not a JSON object")
	}
	var p hookPayload
	if err := json.Unmarshal(trimmed, &p); err != nil {
		return hookPayload{}, fmt.Errorf("codex: malformed hook payload: %s", adapter.HookLine(err.Error()))
	}
	if !validEventName(p.HookEventName) {
		return hookPayload{}, fmt.Errorf("codex: hook payload has no valid hook_event_name (%q)",
			adapter.Bound(adapter.HookLine(p.HookEventName), maxEventName))
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
