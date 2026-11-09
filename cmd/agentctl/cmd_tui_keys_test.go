package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig writes config.yaml where the TUI pane will look for it (the
// isolated tmux server's environment carries the fixture's XDG_CONFIG_HOME).
func writeConfig(t *testing.T, f *cliFixture, content string) {
	t.Helper()
	p := filepath.Join(f.env.Get("XDG_CONFIG_HOME"), "agentctl", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestTUIConfigKeys: on a real terminal, config.yaml keys: rebinds actions
// (the footer and the key both change); a malformed file is a one-line
// banner, the defaults work, and the TUI exits cleanly.
func TestTUIConfigKeys(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	if r := runCmd(t, f.env, f.repo.Root, "new", "one", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}

	writeConfig(t, f, "keys:\n  diff: D\n")
	p := tiStartTUI(t, f.srv, f.env, "keys", f.repo.Root, 120)
	p.waitFor(t, "the rebound footer", func(s string) bool { return strings.Contains(s, "D diff") && strings.Contains(s, "one") })
	f.srv.Run(t, "send-keys", "-t", p.target, "D")
	p.waitFor(t, "the diff view", func(s string) bool { return strings.Contains(s, "diff  one") })
	f.srv.Run(t, "send-keys", "-t", p.target, "Escape")
	p.waitFor(t, "the list again", func(s string) bool { return !strings.Contains(s, "diff  one") })
	f.srv.Run(t, "send-keys", "-t", p.target, "q")
	if status := p.waitExit(t); status != "0" {
		t.Fatalf("exit status %s\n%s", status, p.screen())
	}

	writeConfig(t, f, "keys: [oops\n")
	p = tiStartTUI(t, f.srv, f.env, "badkeys", f.repo.Root, 160)
	p.waitFor(t, "the config banner", func(s string) bool { return strings.Contains(s, "! config.yaml: ") && strings.Contains(s, "d diff") })
	f.srv.Run(t, "send-keys", "-t", p.target, "q")
	if status := p.waitExit(t); status != "0" {
		t.Fatalf("exit status %s\n%s", status, p.screen())
	}
}
