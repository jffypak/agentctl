package claude_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/testenv"
)

func TestLaunchArgvAndNames(t *testing.T) {
	got := claude.LaunchArgv([]string{"/bin/claude"}, "/h/hooks/claude-settings.json", "fix the tests")
	want := []string{"/bin/claude", "--settings", "/h/hooks/claude-settings.json", "fix the tests"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LaunchArgv = %q", got)
	}
	if got := claude.LaunchArgv([]string{"/bin/claude"}, "/s", ""); len(got) != 3 {
		t.Fatalf("empty goal must be omitted: %q", got)
	}
	prefix := []string{"/bin/sh", "-c", "x"}
	_ = claude.LaunchArgv(prefix, "/s", "g")
	if len(prefix) != 3 {
		t.Fatal("LaunchArgv mutated its prefix")
	}
	if claude.SessionName("demo") != "jpark-demo" || claude.SettingsPath("/h") != "/h/hooks/claude-settings.json" {
		t.Fatal("names")
	}
	if claude.New(claude.Config{}).Name() != "claude" {
		t.Fatal("Name")
	}
}

func TestStartLaunchesInWorktreeWithIdentity(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	work, _ := filepath.EvalSymlinks(t.TempDir())
	home := env.Get("AGENTCTL_HOME")
	argvFile := filepath.Join(work, "argv")
	a := claude.New(claude.Config{Runner: srv.Runner(), AgentctlPath: "/opt/agentctl/bin/agentctl"})
	h, err := a.Start(context.Background(), adapter.LaunchSpec{
		TaskID: "demo", Goal: "do it", Worktree: work, Home: home, Socket: srv.Socket,
		AgentCmd: []string{"/bin/sh", "-c", `printf '%s\n' "$@" > "$0"; exec sleep 600`, argvFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.SessionName != "jpark-demo" || h.Identity.Socket != srv.Socket || h.Identity.ServerPID != srv.PID(t) {
		t.Fatalf("handle = %+v", h)
	}
	p, ok := srv.Snapshot(t).Find(h.Identity.PaneID)
	if !ok || p.PanePID != h.Identity.PanePID || p.SessionID != h.Identity.SessionID || p.CurrentPath != work {
		t.Fatalf("pane %+v does not match handle %+v", p, h)
	}
	testenv.WaitFor(t, 0, "argv file", func() (bool, string) {
		b, err := os.ReadFile(argvFile)
		return err == nil && len(b) > 0, string(b)
	})
	b, _ := os.ReadFile(argvFile)
	if want := "--settings\n" + home + "/hooks/claude-settings.json\ndo it\n"; string(b) != want {
		t.Fatalf("agent argv = %q, want %q", b, want)
	}
	if got := srv.Run(t, "show-environment", "-t", h.Identity.SessionID, "AGENTCTL_TASK_ID"); got != "AGENTCTL_TASK_ID=demo" {
		t.Fatalf("env = %q", got)
	}
}

func TestStartValidatesSpec(t *testing.T) {
	a := claude.New(claude.Config{})
	ok := adapter.LaunchSpec{TaskID: "demo", Worktree: "/w", Home: "/h", Socket: "/s", AgentCmd: []string{"/bin/claude"}}
	mods := map[string]func(*adapter.LaunchSpec){
		"bad slug":         func(s *adapter.LaunchSpec) { s.TaskID = "Bad" },
		"relative wt":      func(s *adapter.LaunchSpec) { s.Worktree = "w" },
		"relative home":    func(s *adapter.LaunchSpec) { s.Home = "h" },
		"no socket":        func(s *adapter.LaunchSpec) { s.Socket = "" },
		"no agent cmd":     func(s *adapter.LaunchSpec) { s.AgentCmd = nil },
		"relative agent 0": func(s *adapter.LaunchSpec) { s.AgentCmd = []string{"claude"} },
	}
	for name, mod := range mods {
		spec := ok
		mod(&spec)
		if _, err := a.Start(context.Background(), spec); err == nil || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: Start = %v, want one-line error", name, err)
		}
	}
}
