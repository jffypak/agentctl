package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// EnvStatusLineNested is set for a user status line that `agentctl
// statusline` runs, so a user status line that is itself `agentctl
// statusline` does not run itself again.
const EnvStatusLineNested = "AGENTCTL_STATUSLINE_NESTED"

// StatusLineCommand is the status line command in the launch settings: the
// single-quoted absolute agentctl path followed by " statusline".
func StatusLineCommand(agentctlPath string) string {
	return shellQuote(agentctlPath) + " statusline"
}

// statusInput is the part of the status line input used to find the
// project's settings files.
type statusInput struct {
	Cwd       string `json:"cwd"`
	Workspace struct {
		ProjectDir string `json:"project_dir"`
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
}

// UserStatusLine is the status line command the user configured themselves,
// which the launch settings' --settings status line overrides: the first
// command status line in <project>/.claude/settings.local.json,
// <project>/.claude/settings.json, then $CLAUDE_CONFIG_DIR/settings.json
// (else ~/.claude/settings.json), Claude Code's own precedence order. It is
// "" when there is none, or when that command is agentctl's own status line.
func UserStatusLine(input []byte, getenv func(string) string) string {
	var in statusInput
	_ = json.Unmarshal(input, &in) // no project dir: only the user file is read
	var files []string
	for _, dir := range []string{in.Workspace.ProjectDir, in.Workspace.CurrentDir, in.Cwd} {
		if filepath.IsAbs(dir) {
			files = append(files, filepath.Join(dir, ".claude", "settings.local.json"), filepath.Join(dir, ".claude", "settings.json"))
			break
		}
	}
	if d := getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(d) {
		files = append(files, filepath.Join(d, "settings.json"))
	} else if h := getenv("HOME"); filepath.IsAbs(h) {
		files = append(files, filepath.Join(h, ".claude", "settings.json"))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc struct {
			StatusLine *statusLine `json:"statusLine"`
		}
		if json.Unmarshal(b, &doc) != nil || doc.StatusLine == nil || doc.StatusLine.Type != "command" {
			continue
		}
		cmd := strings.TrimSpace(doc.StatusLine.Command)
		if cmd == "" || isAgentctlStatusLine(cmd) {
			return ""
		}
		return cmd
	}
	return ""
}

// isAgentctlStatusLine spots `…agentctl… statusline` in any quoting.
func isAgentctlStatusLine(cmd string) bool {
	return strings.Contains(cmd, "agentctl") && strings.HasSuffix(strings.TrimSpace(cmd), "statusline")
}
