package tmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// argvRecorder returns an argv prefix whose agent writes each of its
// arguments ($1..) NUL-terminated to out, then sleeps.
func argvRecorder(out string) []string {
	return []string{"/bin/sh", "-c", `for a in "$@"; do printf '%s\0' "$a"; done > "$0.tmp" && mv "$0.tmp" "$0"; exec sleep 600`, out}
}

func readArgv(t *testing.T, path string) []string {
	t.Helper()
	var data []byte
	testenv.WaitFor(t, 0, "agent argv file", func() (bool, string) {
		b, err := os.ReadFile(path)
		data = b
		return err == nil, ""
	})
	parts := strings.Split(string(data), "\x00")
	return parts[:len(parts)-1]
}

func TestNewSessionIntegration(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	ctx := context.Background()
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	dir := filepath.Join(tmp, "work #{pane_id} #(touch pwned-dir)")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(tmp, "argv")
	tricky := []string{"--settings", "/h/s.json", `$(touch pwned) "quoted"`, "ends;", `ends\;`, ";", "#{pane_id}", "multi\nline", "-t"}
	created, err := srv.Client().NewSession(ctx, tmux.NewSessionSpec{
		Name: "agentctl-demo", Dir: dir,
		Env:  []string{"AGENTCTL_TASK_ID=demo", "AGENTCTL_HOME=/home/x;y"},
		Argv: append(argvRecorder(argvFile), tricky...),
	})
	if err != nil {
		t.Fatal(err)
	}

	got := readArgv(t, argvFile)
	if len(got) != len(tricky) {
		t.Fatalf("argv = %q, want %q", got, tricky)
	}
	for i := range tricky {
		if got[i] != tricky[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], tricky[i])
		}
	}
	for _, p := range []string{filepath.Join(dir, "pwned"), filepath.Join(tmp, "pwned"), filepath.Join(tmp, "pwned-dir"), filepath.Join(dir, "pwned-dir")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s exists: something ran through a shell or a format job", p)
		}
	}

	snap := srv.Snapshot(t)
	p, ok := snap.Find(created.PaneID)
	if !ok || p.PanePID != created.PanePID || p.SessionID != created.SessionID || snap.ServerPID != srv.PID(t) || created.ServerPID != snap.ServerPID {
		t.Fatalf("snapshot %+v does not match created %+v", snap, created)
	}
	if p.CurrentPath != dir {
		t.Fatalf("pane cwd = %q, want %q", p.CurrentPath, dir)
	}
	if got := srv.Run(t, "display-message", "-p", "-t", created.PaneID, "#{pane_pid} #{session_name}"); got != strconv.Itoa(created.PanePID)+" agentctl-demo" {
		t.Fatalf("display-message = %q", got)
	}
	if got := srv.Run(t, "show-options", "-w", "-v", "-t", created.SessionID+":", "remain-on-exit"); got != "on" {
		t.Fatalf("remain-on-exit = %q, want on", got)
	}
	for k, want := range map[string]string{"AGENTCTL_TASK_ID": "demo", "AGENTCTL_HOME": "/home/x;y"} {
		if got := srv.Run(t, "show-environment", "-t", created.SessionID, k); got != k+"="+want {
			t.Fatalf("show-environment %s = %q", k, got)
		}
	}

	ok, err = srv.Client().HasSession(ctx, "agentctl-demo")
	if err != nil || !ok {
		t.Fatalf("HasSession exact = %v %v", ok, err)
	}
	ok, err = srv.Client().HasSession(ctx, "agentctl-dem")
	if err != nil || ok {
		t.Fatalf("HasSession prefix = %v %v, want false (no prefix matching)", ok, err)
	}
	if _, err := srv.Client().NewSession(ctx, tmux.NewSessionSpec{Name: "agentctl-demo", Dir: dir, Argv: []string{"/bin/sleep", "600"}}); err == nil ||
		!strings.Contains(err.Error(), "duplicate session") {
		t.Fatalf("duplicate NewSession = %v", err)
	}
}

func TestInstantExitLeavesDeadPaneWithStatus(t *testing.T) {
	t.Parallel()
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	created, err := srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: "exit3", Dir: t.TempDir(), Argv: []string{"/bin/sh", "-c", "exit 3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var p tmux.Pane
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		snap := srv.Snapshot(t)
		p, _ = snap.Find(created.PaneID)
		return p.Dead, ""
	})
	if p.DeadStatus != 3 || p.PanePID != created.PanePID {
		t.Fatalf("dead pane = %+v, want status 3 and the original pane pid", p)
	}
}

func TestSnapshotEmptyKilledAndRestartedServer(t *testing.T) {
	t.Parallel()
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	ctx := context.Background()
	empty := srv.Snapshot(t)
	if empty.ServerPID == 0 || len(empty.Panes) != 0 {
		t.Fatalf("empty server snapshot = %+v", empty)
	}
	first, err := srv.Client().NewSession(ctx, tmux.NewSessionSpec{Name: "a", Dir: t.TempDir(), Argv: []string{"/bin/sleep", "600"}})
	if err != nil {
		t.Fatal(err)
	}
	oldPID := srv.PID(t)

	srv.Kill(t)
	if _, err := srv.Client().Snapshot(ctx); !errors.Is(err, tmux.ErrNoServer) {
		t.Fatalf("snapshot of killed server = %v, want ErrNoServer", err)
	}

	srv.Restart(t)
	second, err := srv.Client().NewSession(ctx, tmux.NewSessionSpec{Name: "b", Dir: t.TempDir(), Argv: []string{"/bin/sleep", "600"}})
	if err != nil {
		t.Fatal(err)
	}
	if second.PaneID != first.PaneID {
		t.Fatalf("restart did not reuse pane id: %s vs %s", second.PaneID, first.PaneID)
	}
	if srv.PID(t) == oldPID {
		t.Fatal("restarted server has the same pid")
	}
}

func TestNewSessionOnUncreatableSocketFails(t *testing.T) {
	t.Parallel()
	testenv.RequireTool(t, "tmux")
	env := testenv.NewEnv(t)
	c := tmux.NewClient(tmux.ExecRunner{Env: env.Strings(), ConfigFile: "/dev/null"},
		// Short path (t.TempDir() exceeds the macOS socket path limit); the directory never exists.
		tmux.Socket{Path: "/tmp/agentctl-missing-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "/sock"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.NewSession(ctx, tmux.NewSessionSpec{Name: "x", Dir: t.TempDir(), Argv: []string{"/bin/sleep", "1"}})
	if err == nil || !strings.Contains(err.Error(), "error creating") {
		t.Fatalf("NewSession on bad socket = %v, want an 'error creating' failure even though tmux exits 0", err)
	}
}
