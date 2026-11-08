package claude_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "hooks", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// TestParseHookTable covers every row of the hook->state table in
// ARCHITECTURE.md §Agent adapter, using payloads shaped like the Claude Code
// 2.1.x hooks reference (testdata/hooks/*.json).
func TestParseHookTable(t *testing.T) {
	const sid = "9f1c2b7e-0a4d-4c55-8e3a-2b6f1d0c9a11"
	cases := []struct {
		fixture string
		event   string
		state   domain.AgentState
		detail  string
		attrs   map[string]string
	}{
		{"session_start_startup", "SessionStart", domain.StateReady, "session started (startup)", map[string]string{"source": "startup"}},
		{"session_start_resume", "SessionStart", domain.StateReady, "session started (resume)", map[string]string{"source": "resume"}},
		{"session_start_clear", "SessionStart", domain.StateReady, "session started (clear)", map[string]string{"source": "clear"}},
		// auto-compact fires mid-turn: ready here would allow a paste into a working agent
		{"session_start_compact", "SessionStart", "", "", map[string]string{"source": "compact"}},
		{"user_prompt_submit", "UserPromptSubmit", domain.StateWorking, "prompt submitted", nil},
		{"pre_tool_use_ask_user_question", "PreToolUse", domain.StateAwaitingInput, "question for you (AskUserQuestion)", map[string]string{"tool_name": "AskUserQuestion"}},
		{"pre_tool_use_bash", "PreToolUse", domain.StateWorking, "tool: Bash", map[string]string{"tool_name": "Bash"}},
		{"post_tool_use", "PostToolUse", domain.StateWorking, "tool done: Write", map[string]string{"tool_name": "Write"}},
		{"pre_compact_auto", "PreCompact", domain.StateWorking, "compacting (auto)", map[string]string{"trigger": "auto"}},
		{"notification_permission_prompt", "Notification", domain.StateAwaitingInput, "permission prompt: Claude needs your permission to use Bash", map[string]string{"notification_type": "permission_prompt"}},
		{"notification_elicitation_dialog", "Notification", domain.StateAwaitingInput, "input requested: MCP server needs input", map[string]string{"notification_type": "elicitation_dialog"}},
		{"notification_idle_prompt", "Notification", domain.StateReady, "idle: Claude is waiting for your input", map[string]string{"notification_type": "idle_prompt"}},
		{"notification_auth_success", "Notification", "", "", map[string]string{"notification_type": "auth_success"}},
		{"permission_request", "PermissionRequest", domain.StateAwaitingInput, "permission request: Bash", map[string]string{"tool_name": "Bash"}},
		{"stop", "Stop", domain.StateReady, "turn finished", nil},
		// SessionEnd never sets the sticky terminal state: reconcile reads the pane's
		// real exit status (exit 3 must read failed), and /clear and /resume fire
		// SessionEnd in a process that keeps running. Detail only.
		{"session_end", "SessionEnd", "", "session ended (prompt_input_exit)", map[string]string{"reason": "prompt_input_exit"}},
		// anything else: recorded, no change
		{"subagent_stop", "SubagentStop", "", "", nil},
		{"stop_failure", "StopFailure", "", "", map[string]string{"error": "rate_limit"}},
	}
	a := claude.New(claude.Config{})
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			obs, err := a.ParseHook(fixture(t, c.fixture), adapter.HookEnv{})
			if err != nil {
				t.Fatal(err)
			}
			if obs.Event != c.event || obs.State != c.state || obs.Detail != c.detail {
				t.Fatalf("obs = {Event:%q State:%q Detail:%q}, want {%q %q %q}", obs.Event, obs.State, obs.Detail, c.event, c.state, c.detail)
			}
			if obs.AgentSessionID != sid || obs.Cwd != "/repos/main-worktrees/demo" {
				t.Fatalf("session/cwd = %q %q", obs.AgentSessionID, obs.Cwd)
			}
			for k, v := range c.attrs {
				if obs.Attrs[k] != v {
					t.Errorf("attr %s = %q, want %q (attrs %v)", k, obs.Attrs[k], v, obs.Attrs)
				}
			}
			if c.event != "UserPromptSubmit" && obs.PromptSHA256 != "" {
				t.Errorf("PromptSHA256 set for %s", c.event)
			}
		})
	}
}

// TestParseHookKeepsOnlyPromptHash: the prompt text never leaves ParseHook;
// neither do tool inputs, tool responses, the transcript path, or assistant text.
func TestParseHookKeepsOnlyPromptHash(t *testing.T) {
	a := claude.New(claude.Config{})
	obs, err := a.ParseHook(fixture(t, "user_prompt_submit"), adapter.HookEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if want := sha("fix the flaky auth tests\nthen run them twice"); obs.PromptSHA256 != want {
		t.Fatalf("PromptSHA256 = %q, want %q", obs.PromptSHA256, want)
	}
	for _, name := range []string{"user_prompt_submit", "post_tool_use", "stop", "pre_tool_use_ask_user_question", "permission_request"} {
		obs, err := a.ParseHook(fixture(t, name), adapter.HookEnv{})
		if err != nil {
			t.Fatal(err)
		}
		all := obs.Event + obs.Detail + obs.Cwd + obs.AgentSessionID
		for k, v := range obs.Attrs {
			all += k + v
		}
		for _, secret := range []string{"flaky auth", "secret-ish", "transcript", ".jsonl", "All tests pass", "Which database", "rm -rf"} {
			if strings.Contains(all, secret) {
				t.Errorf("%s: observation leaks %q: %+v", name, secret, obs)
			}
		}
	}
}

func TestParseHookRejectsMalformed(t *testing.T) {
	a := claude.New(claude.Config{})
	for name, payload := range map[string]string{
		"garbage":          "not json at all",
		"empty":            "",
		"whitespace":       "  \n",
		"array":            `[{"hook_event_name":"Stop"}]`,
		"null":             "null",
		"no event":         `{"session_id":"x"}`,
		"event not str":    `{"hook_event_name":5}`,
		"bad event name":   `{"hook_event_name":"Stop; rm -rf"}`,
		"long event name":  `{"hook_event_name":"` + strings.Repeat("A", 65) + `"}`,
		"truncated":        `{"hook_event_name":"Stop"`,
		"trailing junk":    `{"hook_event_name":"Stop"} {"x":1}`,
		"field wrong type": `{"hook_event_name":"Notification","notification_type":["permission_prompt"]}`,
	} {
		if _, err := a.ParseHook([]byte(payload), adapter.HookEnv{}); err == nil || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: err = %v, want a one-line error", name, err)
		}
	}
}

func TestParseHookSanitizesAndBoundsText(t *testing.T) {
	a := claude.New(claude.Config{})
	msg := "\x1b]52;c;aGVsbG8=\x07evil \x1b[31mred\x1b[0m " + strings.Repeat("x", 1000)
	obs, err := a.ParseHook([]byte(`{"hook_event_name":"Notification","notification_type":"permission_prompt","session_id":"s\u001b[2J","message":`+jsonString(msg)+`}`), adapter.HookEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(obs.Detail, "\x1b\x07") || len(obs.Detail) > claude.MaxHookDetail {
		t.Fatalf("detail not sanitized/bounded: %q (%d bytes)", obs.Detail, len(obs.Detail))
	}
	if !strings.HasPrefix(obs.Detail, "permission prompt: evil red") {
		t.Fatalf("detail = %q", obs.Detail)
	}
	if strings.ContainsAny(obs.AgentSessionID, "\x1b") {
		t.Fatalf("session id not sanitized: %q", obs.AgentSessionID)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteString(`\u00`)
			b.WriteString(hex.EncodeToString([]byte{byte(r)}))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func TestPaneFromEnvAndMatch(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "default")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	realSock, _ := filepath.EvalSymlinks(sock)
	id := domain.Identity{Socket: realSock, ServerPID: 4242, SessionID: "$1", PaneID: "%7", PanePID: 99}

	p, err := claude.PaneFromEnv(adapter.HookEnv{TMUX: filepath.Join(link, "default") + ",4242,1", TMUXPane: "%7"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := p.Matches(id); !ok {
		t.Fatalf("symlinked socket should match: %s", reason)
	}
	for name, env := range map[string]adapter.HookEnv{
		"other pane":   {TMUX: sock + ",4242,1", TMUXPane: "%8"},
		"other server": {TMUX: sock + ",4243,1", TMUXPane: "%7"},
		"other socket": {TMUX: filepath.Join(dir, "other") + ",4242,1", TMUXPane: "%7"},
	} {
		p, err := claude.PaneFromEnv(env)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ok, reason := p.Matches(id); ok || reason == "" {
			t.Errorf("%s: matched (%q)", name, reason)
		}
	}
	for name, env := range map[string]adapter.HookEnv{
		"no TMUX":   {TMUXPane: "%7"},
		"no pane":   {TMUX: sock + ",4242,1"},
		"bad pane":  {TMUX: sock + ",4242,1", TMUXPane: "7"},
		"bad pid":   {TMUX: sock + ",x,1", TMUXPane: "%7"},
		"relative":  {TMUX: "default,4242,1", TMUXPane: "%7"},
		"no fields": {TMUX: sock, TMUXPane: "%7"},
		"zero pid":  {TMUX: sock + ",0,1", TMUXPane: "%7"},
	} {
		if _, err := claude.PaneFromEnv(env); err == nil {
			t.Errorf("%s: PaneFromEnv accepted %+v", name, env)
		}
	}
	// commas in the socket path are split off the right
	p, err = claude.PaneFromEnv(adapter.HookEnv{TMUX: "/tmp/a,b/sock,77,0", TMUXPane: "%1"})
	if err != nil || p.Socket != "/tmp/a,b/sock" || p.ServerPID != 77 || p.PaneID != "%1" {
		t.Fatalf("comma path = %+v %v", p, err)
	}
}
