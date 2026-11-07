package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	cwd, _ := os.Getwd()
	cases := []struct {
		name       string
		env        map[string]string
		home, conf string
	}{
		{"explicit", map[string]string{"AGENTCTL_HOME": "/h", "HOME": "/u", "XDG_STATE_HOME": "/s"}, "/h", "/u/.config/agentctl/config.yaml"},
		{"relative explicit made absolute", map[string]string{"AGENTCTL_HOME": "rel", "HOME": "/u"}, filepath.Join(cwd, "rel"), "/u/.config/agentctl/config.yaml"},
		{"xdg", map[string]string{"HOME": "/u", "XDG_STATE_HOME": "/s", "XDG_CONFIG_HOME": "/c"}, "/s/agentctl", "/c/agentctl/config.yaml"},
		{"relative xdg ignored", map[string]string{"HOME": "/u", "XDG_STATE_HOME": "s", "XDG_CONFIG_HOME": "c"}, "/u/.local/state/agentctl", "/u/.config/agentctl/config.yaml"},
		{"home fallback", map[string]string{"HOME": "/u"}, "/u/.local/state/agentctl", "/u/.config/agentctl/config.yaml"},
	}
	for _, c := range cases {
		p, err := Resolve(env(c.env))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p.Home != c.home || p.ConfigFile != c.conf {
			t.Errorf("%s: home=%q conf=%q, want %q %q", c.name, p.Home, p.ConfigFile, c.home, c.conf)
		}
		if p.DB != filepath.Join(p.Home, "agentctl.db") || p.ClaudeSettings != filepath.Join(p.Home, "hooks", "claude-settings.json") ||
			p.Logs != filepath.Join(p.Home, "logs") {
			t.Errorf("%s: layout = %+v", c.name, p)
		}
	}
	if _, err := Resolve(env(nil)); err == nil {
		t.Fatal("no HOME and no AGENTCTL_HOME accepted")
	}
}

func TestEnsureHomeIsPrivate(t *testing.T) {
	p := ForHome(filepath.Join(t.TempDir(), "h"))
	if err := EnsureHome(p); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{p.Home, p.Logs, p.Hooks} {
		fi, err := os.Stat(d)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, fi.Mode(), err)
		}
	}
}
