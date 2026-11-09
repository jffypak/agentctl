package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// defaultHookTimeout is Claude Code's default for command hooks (seconds).
const defaultHookTimeout = 600

// settings is the part of a Claude Code settings file fakeagent honors:
// {"hooks": {"<Event>": [{"matcher": "...", "hooks": [{"type": "command",
// "command": "...", "timeout": N}]}]}}. Unknown fields are ignored, exactly as
// Claude Code would for a file it does not fully understand.
type settings struct {
	Hooks map[string][]matcherGroup `json:"hooks"`
}

type matcherGroup struct {
	Matcher string        `json:"matcher"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"` // seconds; 0 = defaultHookTimeout
}

func loadSettings(path string) (settings, error) {
	if path == "" {
		return settings{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return settings{}, fmt.Errorf("fakeagent: read --settings: %w", err)
	}
	var s settings
	if err := json.Unmarshal(b, &s); err != nil {
		return settings{}, fmt.Errorf("fakeagent: parse --settings %s: %w", path, err)
	}
	return s, nil
}

// matcherEvents are the events whose matcher filters on a payload field
// (tool_name, notification_type, source, reason, trigger). Every other event
// ignores its matcher and always fires.
var matcherEvents = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "PermissionRequest": true,
	"Notification": true, "SessionStart": true, "SessionEnd": true, "PreCompact": true,
}

// commands returns the command handlers that fire for event, in file order.
// matchValue is the field the event's matcher compares against. An empty or
// "*" matcher matches everything; otherwise it is an anchored regexp
// ("Edit|Write").
func (s settings) commands(event, matchValue string) []hookHandler {
	var out []hookHandler
	for _, g := range s.Hooks[event] {
		if matcherEvents[event] && !matches(g.Matcher, matchValue) {
			continue
		}
		for _, h := range g.Hooks {
			if h.Type == "command" && h.Command != "" {
				out = append(out, h)
			}
		}
	}
	return out
}

func matches(matcher, value string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	re, err := regexp.Compile("^(?:" + matcher + ")$")
	if err != nil {
		return matcher == value
	}
	return re.MatchString(value)
}
