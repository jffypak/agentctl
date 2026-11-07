package testenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/tmux"
)

// TestNewEnvIsHermeticInsideTmux holds whether or not the suite itself runs
// inside the user's tmux (TMUX set in the test process): children never see
// TMUX/TMUX_PANE, and HOME/XDG/AGENTCTL_HOME point at private temp dirs.
func TestNewEnvIsHermeticInsideTmux(t *testing.T) {
	env := NewEnv(t)
	for _, k := range []string{"TMUX", "TMUX_PANE"} {
		if _, ok := env.Lookup(k); ok {
			t.Errorf("%s leaked into the hermetic env", k)
		}
	}
	realHome, _ := os.UserHomeDir()
	for _, k := range []string{"HOME", "AGENTCTL_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME"} {
		v := env.Get(k)
		if v == "" || (realHome != "" && (v == realHome || strings.HasPrefix(v, realHome+"/"))) {
			t.Errorf("%s=%q is not a private temp dir", k, v)
		}
	}
	if env.Get("GIT_CONFIG_GLOBAL") != "/dev/null" || env.Get("GIT_CONFIG_NOSYSTEM") != "1" {
		t.Error("git config not isolated")
	}
	// Without NewTmuxServer, socket resolution must land on a socket that
	// cannot exist, never the user's default server.
	sock, err := tmux.ResolveSocket(env.Getenv, os.Getuid())
	if err != nil || !strings.Contains(sock, "/agentctl-test-no-server-") {
		t.Fatalf("default socket for a server-less env = %q %v", sock, err)
	}
	if _, err := os.Stat(filepath.Dir(sock)); err == nil {
		t.Fatalf("poison socket dir %s exists", filepath.Dir(sock))
	}
}

func TestEnvWithWithoutAreImmutable(t *testing.T) {
	base := Env{"A=1", "B=2"}
	with := base.With("A=3", "C=4")
	without := base.Without("B")
	if base.Get("A") != "1" || len(base) != 2 {
		t.Fatalf("base mutated: %v", base)
	}
	if with.Get("A") != "3" || with.Get("C") != "4" || with.Get("B") != "2" {
		t.Fatalf("with = %v", with)
	}
	if _, ok := without.Lookup("B"); ok || without.Get("A") != "1" {
		t.Fatalf("without = %v", without)
	}
}

func TestBinDirExposesOnlyNamedTools(t *testing.T) {
	dir := BinDir(t, "sh")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "sh" {
		t.Fatalf("BinDir entries = %v %v", entries, err)
	}
}

func TestTmuxServerIsIsolatedAndCleanedUp(t *testing.T) {
	var sock string
	t.Run("server", func(t *testing.T) {
		srv := NewTmuxServer(t, NewEnv(t))
		sock = srv.Socket
		if !strings.HasPrefix(filepath.Base(sock), "agentctl-test-") || srv.Env.Get(tmux.EnvSocket) != sock {
			t.Fatalf("server = %+v", srv)
		}
		if def := os.Getenv("TMUX"); def != "" && tmux.SameSocket(tmux.SocketFromTMUX(def), sock) {
			t.Fatal("test server is the user's server")
		}
		if srv.PID(t) <= 0 {
			t.Fatal("no pid")
		}
	})
	if sock == "" {
		return // skipped: tmux missing
	}
	c := tmux.NewClient(tmux.ExecRunner{Env: NewEnv(t).Strings()}, tmux.Socket{Path: sock})
	if _, err := c.ServerPID(context.Background()); err == nil {
		t.Fatal("server still running after cleanup")
	}
	if _, err := os.Lstat(sock); err == nil {
		t.Fatal("socket file left behind")
	}
}

func TestWaitFor(t *testing.T) {
	n := 0
	WaitFor(t, 0, "counter", func() (bool, string) { n++; return n >= 3, "" })
	if n < 3 {
		t.Fatal("WaitFor returned early")
	}
	if Eventually(20*time.Millisecond, func() (bool, string) { return false, "" }) {
		t.Fatal("Eventually reported success")
	}
}
