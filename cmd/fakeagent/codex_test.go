package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/testenv"
)

// TestParseCodexArgs is the contract test for Codex mode: fakeagent finds
// exactly the codex adapter's one command hook (timeout 5) for every
// subscribed event from the -c flags agentctl emits, keeps the positional
// prompt, and ignores -c values that are not hooks.
func TestParseCodexArgs(t *testing.T) {
	const exe = `/opt/agent "ctl"/bin/agentctl`
	flags, err := codex.HookFlags(exe)
	if err != nil {
		t.Fatal(err)
	}
	args := append(append([]string{"--codex", "-c", `model="x"`}, flags...), "do it")
	cfg, err := parseConfig(args, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.codex || cfg.initialPrompt != "do it" {
		t.Fatalf("cfg codex %v prompt %q", cfg.codex, cfg.initialPrompt)
	}
	for _, ev := range codex.HookEvents() {
		hs := cfg.codexHooks.commands(ev, "")
		if len(hs) != 1 || hs[0].Command != codex.HookCommand(exe) || hs[0].Timeout != codex.HookTimeout(ev) {
			t.Fatalf("%s: handlers = %+v", ev, hs)
		}
	}
	if hs := cfg.codexHooks.commands("Notification", ""); len(hs) != 0 {
		t.Fatalf("unsubscribed event has handlers: %+v", hs)
	}
	if cfg, err := parseConfig([]string{"--codex"}, func(string) string { return "" }); err != nil || cfg.initialPrompt != "" {
		t.Fatalf("no prompt: %+v %v", cfg, err)
	}
}

func TestCodexModeRejectsUnknownHookShape(t *testing.T) {
	for _, args := range [][]string{
		{"--codex", "-c", "hooks.Stop=garbage"},
		{"--codex", "-c", `hooks.Stop=[{hooks=[{type="prompt",command="x",timeout=5}]}]`},
		{"--codex", "-c"},
		{"--codex", "a", "b"},
	} {
		if _, err := parseConfig(args, func(string) string { return "" }); err == nil {
			t.Errorf("parseConfig(%q) accepted", args)
		}
	}
}

// startCodexAgent runs fakeagent in Codex mode with every subscribed event
// recording its payload (like startAgent for Claude mode).
func startCodexAgent(t *testing.T, prompt string, mod func(*config)) *harness {
	t.Helper()
	return startAgent(t, prompt, func(c *config) {
		dir := filepath.Dir(c.logPath)
		rec := "cat >> '" + filepath.Join(dir, "hooks.jsonl") + "'; echo >> '" + filepath.Join(dir, "hooks.jsonl") + "'"
		c.codex, c.settingsPath = true, ""
		c.codexHooks = settings{Hooks: map[string][]matcherGroup{}}
		for _, ev := range codex.HookEvents() {
			c.codexHooks.Hooks[ev] = []matcherGroup{{Hooks: []hookHandler{{Type: "command", Command: rec, Timeout: 5}}}}
		}
		if mod != nil {
			mod(c)
		}
	})
}

// TestCodexModeLateSessionStart: like real Codex, no hook fires when the TUI
// opens; SessionStart(startup) fires right before the first prompt's
// UserPromptSubmit, and turn-scoped payloads carry turn_id and model.
func TestCodexModeLateSessionStart(t *testing.T) {
	h := startCodexAgent(t, "", nil)
	h.waitReady()
	time.Sleep(100 * time.Millisecond) // a hook fired now would break the Codex contract
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("hooks fired before the first prompt: %v", ev)
	}
	h.send("hi\r")
	h.waitEvents("SessionStart:startup UserPromptSubmit PreToolUse:Bash PostToolUse:Bash Stop")
	h.waitReady()
	payloads := readJSONL(t, filepath.Join(h.dir, "hooks.jsonl"))
	for _, p := range payloads[1:] {
		if p["turn_id"] == nil || p["turn_id"] == "" || p["model"] == nil {
			t.Fatalf("turn-scoped payload lacks turn_id/model: %v", p)
		}
	}
	if payloads[0]["turn_id"] != nil {
		t.Errorf("SessionStart carries a turn_id: %v", payloads[0])
	}
	// a second turn has no second SessionStart
	h.send("again\r")
	h.waitEvents("SessionStart:startup UserPromptSubmit PreToolUse:Bash PostToolUse:Bash Stop UserPromptSubmit PreToolUse:Bash PostToolUse:Bash Stop")
}

// TestCodexModeAskFiresPermissionRequestOnly: Codex has no Notification
// event; #ask fires PreToolUse + PermissionRequest and waits for y.
func TestCodexModeAskFiresPermissionRequestOnly(t *testing.T) {
	h := startCodexAgent(t, "#ask", nil)
	h.waitEvents("SessionStart:startup UserPromptSubmit PreToolUse:Bash PermissionRequest:Bash")
	h.send("y")
	h.waitEvents("SessionStart:startup UserPromptSubmit PreToolUse:Bash PermissionRequest:Bash PostToolUse:Bash PreToolUse:Bash PostToolUse:Bash Stop")
}

// TestCodexModeHookTrustGate: FAKEAGENT_HOOK_TRUST=1 shows Codex's
// hook-trust screen and fires nothing until it is answered; "2" trusts.
func TestCodexModeHookTrustGate(t *testing.T) {
	h := startCodexAgent(t, "hello", func(c *config) { c.hookTrust = true })
	testenv.WaitFor(t, 0, "hook trust screen", func() (bool, string) {
		s := h.out.String()
		return strings.Contains(s, "Review hooks") && strings.Contains(s, "Trust all and continue") &&
			strings.Contains(s, "Continue without trusting (hooks won't run)"), s
	})
	time.Sleep(100 * time.Millisecond)
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("hooks fired before trust was answered: %v", ev)
	}
	h.send("2")
	h.waitEvents("SessionStart:startup UserPromptSubmit PreToolUse:Bash PostToolUse:Bash Stop")
}

// "3" continues without trusting: the agent works, but no hook ever fires.
func TestCodexModeHookTrustContinueWithoutHooks(t *testing.T) {
	h := startCodexAgent(t, "hello", func(c *config) { c.hookTrust = true })
	testenv.WaitFor(t, 0, "hook trust screen", func() (bool, string) {
		return strings.Contains(h.out.String(), "Continue without trusting"), h.out.String()
	})
	h.send("3")
	h.waitReady()
	h.send("more\r")
	testenv.WaitFor(t, 0, "second turn done", func() (bool, string) {
		return strings.Count(h.out.String(), "done\n") == 2, h.out.String()
	})
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("hooks fired without trust: %v", ev)
	}
}

func TestParseConfigHookTrust(t *testing.T) {
	cfg, err := parseConfig([]string{"--codex"}, func(k string) string { return map[string]string{"FAKEAGENT_HOOK_TRUST": "1"}[k] })
	if err != nil || !cfg.hookTrust {
		t.Fatalf("hookTrust = %v %v", cfg.hookTrust, err)
	}
}
