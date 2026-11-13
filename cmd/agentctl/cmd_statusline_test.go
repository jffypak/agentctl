package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/quota"
)

const statusWithLimits = `{"cwd":"%s","workspace":{"project_dir":"%s"},"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":1794557822},"seven_day":{"used_percentage":18,"resets_at":1794841357}}}`

func runStatusLine(t *testing.T, input string, env []string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	sys := &system{
		stdin: strings.NewReader(input), stdout: &out, stderr: &bytes.Buffer{},
		environ: env, cwd: t.TempDir(), now: func() time.Time { return time.Unix(1794550000, 0) },
		executable: "/opt/agentctl/bin/agentctl",
	}
	code := run(context.Background(), []string{"statusline"}, sys)
	return code, out.String()
}

func writeSettings(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStatusLineSavesQuotaAndPrintsNothing(t *testing.T) {
	home, userHome, project := t.TempDir(), t.TempDir(), t.TempDir()
	env := []string{"AGENTCTL_HOME=" + home, "HOME=" + userHome}
	code, out := runStatusLine(t, strings.ReplaceAll(statusWithLimits, "%s", project), env)
	if code != exitOK || out != "" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	snap, ok, err := quota.ReadClaude(home)
	if err != nil || !ok || snap.FiveHour.UsedPercent != 42 || snap.Weekly.UsedPercent != 18 {
		t.Fatalf("saved = %+v ok=%v err=%v", snap, ok, err)
	}

	// an input without rate limits keeps the saved snapshot
	if code, _ := runStatusLine(t, `{"cwd":"/"}`, env); code != exitOK {
		t.Fatalf("code=%d", code)
	}
	if snap, ok, _ := quota.ReadClaude(home); !ok || snap.FiveHour.UsedPercent != 42 {
		t.Fatalf("snapshot lost: %+v", snap)
	}
}

func TestStatusLineRunsTheUsersOwnStatusLine(t *testing.T) {
	home, userHome, project := t.TempDir(), t.TempDir(), t.TempDir()
	env := []string{"AGENTCTL_HOME=" + home, "HOME=" + userHome, "PATH=" + os.Getenv("PATH")}
	input := strings.ReplaceAll(statusWithLimits, "%s", project)

	writeSettings(t, filepath.Join(userHome, ".claude", "settings.json"),
		`{"statusLine":{"type":"command","command":"printf user-line"}}`)
	if _, out := runStatusLine(t, input, env); out != "user-line" {
		t.Fatalf("user status line: out=%q", out)
	}

	// the project's settings win over the user's, and the command gets the input
	writeSettings(t, filepath.Join(project, ".claude", "settings.json"),
		`{"hooks":{"Stop":"not the usual shape"},"statusLine":{"type":"command","command":"grep -c rate_limits"}}`)
	if _, out := runStatusLine(t, input, env); out != "1\n" {
		t.Fatalf("project status line: out=%q", out)
	}

	// agentctl's own status line in the user's settings is never run again
	writeSettings(t, filepath.Join(project, ".claude", "settings.local.json"),
		`{"statusLine":{"type":"command","command":"'/opt/agentctl/bin/agentctl' statusline"}}`)
	if _, out := runStatusLine(t, input, env); out != "" {
		t.Fatalf("own status line ran: out=%q", out)
	}
	if _, out := runStatusLine(t, input, append(env, "AGENTCTL_STATUSLINE_NESTED=1")); out != "" {
		t.Fatalf("nested run printed %q", out)
	}
}
