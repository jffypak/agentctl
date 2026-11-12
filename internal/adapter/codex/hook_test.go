package codex_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/domain"
)

// stopPayload is a real Codex CLI 0.167.2 Stop payload (2026-11-12 probe).
const stopPayload = `{"session_id":"01a1d138-cca2-7633-8ea1-372bab0d6d34","turn_id":"01a1d138-eef4-70b1-a0e4-78d8ebbba83f",` +
	`"transcript_path":"/Users/u/.codex/sessions/2026/11/12/rollout-x.jsonl","cwd":"/w","hook_event_name":"Stop",` +
	`"model":"gpt-6-sol","permission_mode":"default","stop_hook_active":false,"last_assistant_message":"SECRET-ANSWER"}`

func payload(event, extra string) string {
	p := `{"session_id":"s1","turn_id":"t1","transcript_path":"/tmp/SECRET-PATH.jsonl","cwd":"/w","hook_event_name":"` + event +
		`","model":"gpt-6-sol","permission_mode":"default"`
	if extra != "" {
		p += "," + extra
	}
	return p + "}"
}

func TestCodexParseHook(t *testing.T) {
	cases := []struct {
		name, body string
		state      domain.AgentState
		detail     string
	}{
		{"start", payload("SessionStart", `"source":"startup"`), "", "session started (startup)"},
		{"start compact", payload("SessionStart", `"source":"compact"`), "", ""},
		{"prompt", payload("UserPromptSubmit", `"prompt":"hi"`), domain.StateWorking, "prompt submitted"},
		{"pre tool", payload("PreToolUse", `"tool_name":"Bash","tool_use_id":"u","tool_input":{"command":"SECRET-CMD"}`), domain.StateWorking, "tool: Bash"},
		{"post tool", payload("PostToolUse", `"tool_name":"Bash","tool_response":"SECRET-OUT"`), domain.StateWorking, "tool done: Bash"},
		{"compact", payload("PreCompact", `"trigger":"auto"`), domain.StateWorking, "compacting (auto)"},
		{"permission", payload("PermissionRequest", `"tool_name":"Bash","tool_input":{"description":null}`), domain.StateAwaitingInput, "permission request: Bash"},
		{"stop", stopPayload, domain.StateReady, "turn finished"},
		{"interrupt", payload("Interrupt", ""), domain.StateReady, "interrupted"},
		{"end", payload("SessionEnd", `"reason":"other"`), "", "session ended (other)"},
		{"model request", payload("PostModelRequest", `"status":"ok"`), "", ""},
	}
	a := codex.New(codex.Config{})
	for _, c := range cases {
		obs, err := a.ParseHook([]byte(c.body), adapter.HookEnv{})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if obs.State != c.state || obs.Detail != c.detail {
			t.Errorf("%s: state %q detail %q, want %q %q", c.name, obs.State, obs.Detail, c.state, c.detail)
		}
		for k, v := range obs.Attrs {
			if strings.Contains(v, "SECRET") {
				t.Errorf("%s: attr %s leaks %q", c.name, k, v)
			}
		}
		if strings.Contains(obs.Detail, "SECRET") || strings.Contains(obs.Cwd, "SECRET") {
			t.Errorf("%s: observation leaks a secret field: %+v", c.name, obs)
		}
	}
	obs, _ := a.ParseHook([]byte(stopPayload), adapter.HookEnv{})
	if obs.AgentSessionID != "01a1d138-cca2-7633-8ea1-372bab0d6d34" || obs.Event != "Stop" || obs.Cwd != "/w" {
		t.Errorf("stop observation = %+v", obs)
	}
	obs, _ = a.ParseHook([]byte(payload("UserPromptSubmit", `"prompt":"hi"`)), adapter.HookEnv{})
	sum := sha256.Sum256([]byte("hi"))
	if obs.PromptSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("PromptSHA256 = %q", obs.PromptSHA256)
	}
	if obs.Attrs["source"] != "" || obs.Attrs["tool_name"] != "" {
		t.Errorf("empty attrs kept: %v", obs.Attrs)
	}
	obs, _ = a.ParseHook([]byte(payload("SessionStart", `"source":"clear"`)), adapter.HookEnv{})
	if obs.Attrs["source"] != "clear" {
		t.Errorf("source attr = %v", obs.Attrs)
	}
}

func TestCodexParseHookRejectsMalformed(t *testing.T) {
	a := codex.New(codex.Config{})
	for _, body := range []string{"", "  ", "[]", "{", `{"session_id":"s"}`, `{"hook_event_name":"Bad Name"}`} {
		if _, err := a.ParseHook([]byte(body), adapter.HookEnv{}); err == nil || strings.Contains(err.Error(), "\n") {
			t.Errorf("ParseHook(%q) = %v, want a one-line error", body, err)
		}
	}
}
