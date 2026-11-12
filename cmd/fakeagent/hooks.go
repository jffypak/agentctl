package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"time"
)

// matchField names the payload field an event's matcher compares against.
var matchField = map[string]string{
	"PreToolUse": "tool_name", "PostToolUse": "tool_name", "PermissionRequest": "tool_name",
	"Notification": "notification_type", "SessionStart": "source", "SessionEnd": "reason", "PreCompact": "trigger",
}

// hookRun is one hook command execution, appended to FAKEAGENT_HOOK_LOG.
type hookRun struct {
	TS       string `json:"ts"`
	Event    string `json:"event"`
	Command  string `json:"command"`
	Exit     int    `json:"exit"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Millis   int64  `json:"ms"`
	TimedOut bool   `json:"timed_out,omitempty"`
}

// fire runs every command hook configured for event, one after another, the
// way Claude Code does: payload JSON on stdin (common fields + fields), the
// agent's environment plus CLAUDE_PROJECT_DIR, cwd = the agent's cwd, killed
// after the handler's timeout. It returns only when every hook has returned.
func (a *agent) fire(event string, fields map[string]any) {
	payload := map[string]any{
		"session_id":      a.sessionID,
		"transcript_path": filepath.Join(a.cfg.transcriptDir, a.sessionID+".jsonl"),
		"cwd":             a.cfg.cwd,
		"permission_mode": "default",
		"hook_event_name": event,
	}
	if a.cfg.codex {
		payload["model"] = "fakeagent-codex"
		if event != "SessionStart" && event != "SessionEnd" && a.turnID != "" {
			payload["turn_id"] = a.turnID
		}
	}
	for k, v := range fields {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	matchValue, _ := fields[matchField[event]].(string)
	for _, h := range a.hooks.commands(event, matchValue) {
		a.hookLog.write(a.runHook(event, h, body))
	}
}

func (a *agent) runHook(event string, h hookHandler, body []byte) hookRun {
	timeout := time.Duration(h.Timeout) * time.Second
	if h.Timeout <= 0 {
		timeout = defaultHookTimeout * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", h.Command)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Dir = a.cfg.cwd
	cmd.Env = append(append([]string(nil), a.cfg.env...), "CLAUDE_PROJECT_DIR="+a.cfg.cwd)
	start := time.Now()
	err := cmd.Run()
	r := hookRun{
		TS: start.UTC().Format(time.RFC3339Nano), Event: event, Command: h.Command,
		Stdout: stdout.String(), Stderr: stderr.String(), Millis: time.Since(start).Milliseconds(),
	}
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		r.TimedOut, r.Exit = true, -1
	case errors.As(err, &ee):
		r.Exit = ee.ExitCode()
	case err != nil:
		r.Exit, r.Stderr = -1, err.Error()
	}
	return r
}
