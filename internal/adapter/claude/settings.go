package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HookTimeoutSeconds is the per-hook timeout written into the settings. The
// hook itself finishes within HookBudget (< 2 s); 5 s is the safety net.
const HookTimeoutSeconds = 5

// hookEvents are the Claude Code hook events agentctl subscribes to: every row
// of the hook->state table in ARCHITECTURE.md §Agent adapter.
var hookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact",
	"Notification", "PermissionRequest", "Stop", "StopFailure", "SessionEnd",
}

// HookEvents lists the subscribed hook events in table order (a copy).
func HookEvents() []string { return append([]string(nil), hookEvents...) }

// HookCommand is the shell command a hook runs: the single-quoted absolute
// agentctl path followed by " hook". Claude Code runs it through a shell, so
// the path is quoted even when it looks safe.
func HookCommand(agentctlPath string) string {
	return shellQuote(agentctlPath) + " hook"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// settingsFile is the generated Claude Code settings: hooks only. No matcher
// means "match everything" for every event.
type settingsFile struct {
	Hooks map[string][]matcherGroup `json:"hooks"`
}

type matcherGroup struct {
	Hooks []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// SettingsJSON renders the settings file for agentctlPath. It is
// deterministic (sorted keys, two-space indent, trailing newline), so the same
// path always yields the same bytes. `agentctl hooks-snippet` prints exactly
// this for users to merge into their own Claude settings.
func SettingsJSON(agentctlPath string) ([]byte, error) {
	if err := validateAgentctlPath(agentctlPath); err != nil {
		return nil, err
	}
	doc := settingsFile{Hooks: map[string][]matcherGroup{}}
	for _, ev := range hookEvents {
		doc.Hooks[ev] = []matcherGroup{{Hooks: []hookHandler{{
			Type: "command", Command: HookCommand(agentctlPath), Timeout: HookTimeoutSeconds,
		}}}}
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("claude: render hook settings: %w", err)
	}
	return append(b, '\n'), nil
}

func validateAgentctlPath(p string) error {
	switch {
	case p == "":
		return errors.New("claude: agentctl path for the hook command is unknown")
	case !filepath.IsAbs(p):
		return fmt.Errorf("claude: agentctl path %q is not absolute", p)
	case strings.ContainsAny(p, "\x00\n\r"):
		return fmt.Errorf("claude: agentctl path %q contains a control character", p)
	}
	return nil
}

// EnsureSettings makes sure <home>/hooks/claude-settings.json exists and is
// current, and returns its path. Start calls it before every launch.
//
// The file holds one command hook per mapped event (HookCommand of
// a.cfg.AgentctlPath, timeout 5). When the file already has exactly the
// rendered bytes and mode 0600 it is left alone; otherwise it is written to a
// temp file in the same directory (mode 0600) and renamed over the old one,
// so a concurrent reader (a launching claude) never sees a partial file.
func (a *Adapter) EnsureSettings(home string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("claude: agentctl home %q is not absolute", home)
	}
	want, err := SettingsJSON(a.cfg.AgentctlPath)
	if err != nil {
		return "", err
	}
	path := SettingsPath(home)
	if current(path, want) {
		return path, nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("claude: create %s: %w", dir, err)
	}
	if err := writeAtomic(path, want); err != nil {
		return "", err
	}
	return path, nil
}

// current reports whether path already holds exactly want with mode 0600.
func current(path string, want []byte) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		return false
	}
	got, err := os.ReadFile(path)
	return err == nil && bytes.Equal(got, want)
}

func writeAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("claude: write hook settings: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("claude: write hook settings: %w", err)
	}
	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("claude: write hook settings: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("claude: write hook settings: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("claude: write hook settings: %w", err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("claude: install hook settings: %w", err)
	}
	return nil
}
