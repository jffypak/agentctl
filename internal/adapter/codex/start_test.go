package codex_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/testenv"
)

func TestCodexHookCommandAndEvents(t *testing.T) {
	if got := codex.HookCommand("/opt/agentctl"); got != `'/opt/agentctl' hook --agent codex` {
		t.Fatalf("HookCommand = %q", got)
	}
	want := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest",
		"PreCompact", "Stop", "Interrupt", "SessionEnd"}
	ev := codex.HookEvents()
	if !reflect.DeepEqual(ev, want) {
		t.Fatalf("HookEvents = %v", ev)
	}
	ev[0] = "x"
	if codex.HookEvents()[0] != "SessionStart" {
		t.Fatal("HookEvents returned its backing array")
	}
	if codex.Name != "codex" || codex.New(codex.Config{}).Name() != "codex" {
		t.Fatal("Name")
	}
}

func TestCodexHookFlags(t *testing.T) {
	flags, err := codex.HookFlags("/opt/agentctl")
	if err != nil {
		t.Fatal(err)
	}
	if len(flags) != 18 {
		t.Fatalf("len = %d: %q", len(flags), flags)
	}
	if flags[0] != "-c" || flags[1] != `hooks.SessionStart=[{hooks=[{type="command",command="'/opt/agentctl' hook --agent codex",timeout=5}]}]` {
		t.Fatalf("first flag = %q %q", flags[0], flags[1])
	}
	for i := 0; i < len(flags); i += 2 {
		if flags[i] != "-c" || !strings.HasPrefix(flags[i+1], "hooks."+codex.HookEvents()[i/2]+"=") {
			t.Errorf("flag %d = %q %q", i, flags[i], flags[i+1])
		}
	}
	again, _ := codex.HookFlags("/opt/agentctl")
	if !reflect.DeepEqual(flags, again) {
		t.Fatal("HookFlags is not deterministic (Codex hook trust is keyed by the command hash)")
	}
	odd, err := codex.HookFlags(`/opt/a"b\c`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `command="'/opt/a\"b\\c' hook --agent codex"`; !strings.Contains(odd[1], want) {
		t.Fatalf("TOML escaping: %q lacks %q", odd[1], want)
	}
	for _, bad := range []string{"", "agentctl", "/a\nb", "/a\x00b"} {
		if _, err := codex.HookFlags(bad); err == nil {
			t.Errorf("HookFlags(%q) accepted", bad)
		}
	}
}

func TestCodexLaunchArgv(t *testing.T) {
	prefix := []string{"/usr/bin/codex"}
	argv, err := codex.LaunchArgv(prefix, "/opt/agentctl", "do x")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 1+18+1 || argv[0] != "/usr/bin/codex" || argv[len(argv)-1] != "do x" || argv[1] != "-c" {
		t.Fatalf("argv = %q", argv)
	}
	argv, _ = codex.LaunchArgv(prefix, "/opt/agentctl", "")
	if len(argv) != 19 || argv[len(argv)-1] == "" {
		t.Fatalf("empty goal must be omitted: %q", argv)
	}
	if len(prefix) != 1 {
		t.Fatal("LaunchArgv mutated its prefix")
	}
}

func TestCodexStartLaunchesInWorktreeWithIdentity(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	work, _ := filepath.EvalSymlinks(t.TempDir())
	home := env.Get("AGENTCTL_HOME")
	argvFile := filepath.Join(work, "argv")
	a := codex.New(codex.Config{Runner: srv.Runner(), AgentctlPath: "/opt/agentctl/bin/agentctl"})
	h, err := a.Start(context.Background(), adapter.LaunchSpec{
		TaskID: "demo", Goal: "do it", Worktree: work, Home: home, Socket: srv.Socket,
		AgentCmd: []string{"/bin/sh", "-c", `printf '%s\n' "$@" > "$0"; exec sleep 600`, argvFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.SessionName != "agentctl-demo" || h.Identity.Socket != srv.Socket || h.Identity.ServerPID != srv.PID(t) {
		t.Fatalf("handle = %+v", h)
	}
	p, ok := srv.Snapshot(t).Find(h.Identity.PaneID)
	if !ok || p.PanePID != h.Identity.PanePID || p.CurrentPath != work {
		t.Fatalf("pane %+v does not match handle %+v", p, h)
	}
	testenv.WaitFor(t, 0, "argv file", func() (bool, string) {
		b, err := os.ReadFile(argvFile)
		return err == nil && len(b) > 0, string(b)
	})
	b, _ := os.ReadFile(argvFile)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	flags, _ := codex.HookFlags("/opt/agentctl/bin/agentctl")
	if want := append(append([]string(nil), flags...), "do it"); !reflect.DeepEqual(lines, want) {
		t.Fatalf("agent argv = %q, want %q", lines, want)
	}
	if got := srv.Run(t, "show-environment", "-t", h.Identity.SessionID, "AGENTCTL_TASK_ID"); got != "AGENTCTL_TASK_ID=demo" {
		t.Fatalf("env = %q", got)
	}
	if got := srv.Run(t, "show-options", "-w", "-v", "-t", h.Identity.PaneID, "remain-on-exit"); got != "on" {
		t.Fatalf("remain-on-exit = %q", got)
	}
}

func TestCodexStartValidatesSpec(t *testing.T) {
	a := codex.New(codex.Config{AgentctlPath: "/opt/agentctl"})
	ok := adapter.LaunchSpec{TaskID: "demo", Worktree: "/w", Home: "/h", Socket: "/s", AgentCmd: []string{"/bin/codex"}}
	mods := map[string]func(*adapter.LaunchSpec){
		"bad slug":         func(s *adapter.LaunchSpec) { s.TaskID = "Bad" },
		"relative wt":      func(s *adapter.LaunchSpec) { s.Worktree = "w" },
		"no socket":        func(s *adapter.LaunchSpec) { s.Socket = "" },
		"no agent cmd":     func(s *adapter.LaunchSpec) { s.AgentCmd = nil },
		"relative agent 0": func(s *adapter.LaunchSpec) { s.AgentCmd = []string{"codex"} },
	}
	for name, mod := range mods {
		spec := ok
		mod(&spec)
		if _, err := a.Start(context.Background(), spec); err == nil || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: Start = %v, want one-line error", name, err)
		}
	}
	if _, err := codex.New(codex.Config{}).Start(context.Background(), ok); err == nil {
		t.Error("Start without an agentctl path accepted (hooks would have no command)")
	}
}
