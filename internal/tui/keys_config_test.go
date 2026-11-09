package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
)

func TestKeyMapFromConfigAppliesOverrides(t *testing.T) {
	t.Parallel()
	km, err := KeyMapFromConfig(map[string][]string{"diff": {"D"}, "logs": {"L", "l"}, "confirm": {"Y"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := km.Diff.Keys(); !slices.Equal(got, []string{"D"}) || km.Diff.Help().Key != "D" || km.Diff.Help().Desc != "diff" {
		t.Fatalf("diff = %v %+v", got, km.Diff.Help())
	}
	if got := km.Logs.Keys(); !slices.Equal(got, []string{"L", "l"}) {
		t.Fatalf("logs = %v", got)
	}
	if got := km.Confirm.Keys(); !slices.Equal(got, []string{"Y"}) {
		t.Fatalf("confirm = %v", got)
	}
	def := DefaultKeyMap()
	if !slices.Equal(km.Prompt.Keys(), def.Prompt.Keys()) || !slices.Equal(km.Open.Keys(), def.Open.Keys()) {
		t.Fatal("unlisted actions lost their defaults")
	}
	// duplicates inside one binding are harmless
	km, err = KeyMapFromConfig(map[string][]string{"diff": {"D", "D"}})
	if err != nil || !slices.Equal(km.Diff.Keys(), []string{"D"}) {
		t.Fatalf("dup = %v %v", km.Diff.Keys(), err)
	}
	if km, err := KeyMapFromConfig(nil); err != nil || !slices.Equal(km.Diff.Keys(), def.Diff.Keys()) {
		t.Fatalf("nil config = %v", err)
	}
}

func TestKeyMapFromConfigProblemsFallBackToDefaults(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		keys map[string][]string
		want string
	}{
		{"unknown action", map[string][]string{"dif": {"D"}, "diff": {"z"}}, `unknown action "dif"`},
		{"conflict with a default", map[string][]string{"diff": {"l"}}, `"l" is bound to both diff and logs`},
		{"conflict between overrides", map[string][]string{"diff": {"z"}, "new": {"z"}}, `"z" is bound to both diff and new`},
		{"empty binding", map[string][]string{"diff": {}}, `diff has no keys`},
		{"blank key", map[string][]string{"diff": {" "}}, `invalid key " " for diff`},
		{"key with spaces", map[string][]string{"diff": {"ctrl d"}}, `invalid key "ctrl d" for diff`},
		{"confirm on enter", map[string][]string{"open": {"o"}, "confirm": {"enter"}}, `confirm cannot use "enter"`},
		{"confirm on esc", map[string][]string{"back": {"q"}, "quit": {"Q"}, "confirm": {"esc"}}, `confirm cannot use "esc"`},
	}
	for _, c := range cases {
		km, err := KeyMapFromConfig(c.keys)
		if err == nil {
			t.Errorf("%s: accepted %v", c.name, c.keys)
			continue
		}
		if !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: error %q, want it to contain %q on one line", c.name, err, c.want)
		}
		def := DefaultKeyMap()
		for _, a := range Actions() {
			got, _ := km.Binding(a)
			want, _ := def.Binding(a)
			if !slices.Equal(got.Keys(), want.Keys()) {
				t.Errorf("%s: %s = %v, want the default %v", c.name, a, got.Keys(), want.Keys())
			}
		}
	}
	// several problems: all named, still one line, deterministic order
	_, err := KeyMapFromConfig(map[string][]string{"zz": {"1"}, "aa": {"2"}, "diff": {"l"}})
	want := `config.yaml keys: unknown action "aa"; unknown action "zz"; "l" is bound to both diff and logs`
	if err == nil || err.Error() != want {
		t.Fatalf("several problems = %q, want %q", err, want)
	}
}

func TestDefaultKeyMapHasNoConflicts(t *testing.T) {
	t.Parallel()
	if _, err := KeyMapFromConfig(map[string][]string{}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	def := DefaultKeyMap()
	for _, a := range Actions() {
		b, _ := def.Binding(a)
		for _, k := range b.Keys() {
			if prev, dup := seen[k]; dup {
				t.Fatalf("default %q is bound to %s and %s", k, prev, a)
			}
			seen[k] = a
		}
	}
}

// configEnv is a hermetic env with config.yaml holding content ("" = no file).
func configEnv(t *testing.T, content string) testenv.Env {
	t.Helper()
	env := testenv.NewEnv(t)
	if content == "" {
		return env
	}
	p := filepath.Join(env.Get("XDG_CONFIG_HOME"), "agentctl", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestConfigKeyMap(t *testing.T) {
	t.Parallel()
	// missing file: defaults (nil), no warning, an earlier warning kept
	km, warn := ConfigKeyMap(configEnv(t, "").Getenv, "")
	if km != nil || warn != "" {
		t.Fatalf("missing file = %v %q", km, warn)
	}
	if _, warn := ConfigKeyMap(configEnv(t, "").Getenv, "tmux binding unavailable"); warn != "tmux binding unavailable" {
		t.Fatalf("earlier warning = %q", warn)
	}

	// overrides apply
	km, warn = ConfigKeyMap(configEnv(t, "keys:\n  diff: D\n").Getenv, "")
	if km == nil || warn != "" || !slices.Equal(km.Diff.Keys(), []string{"D"}) {
		t.Fatalf("override = %v %q", km, warn)
	}

	// unknown action / conflict: one-line warning, defaults
	env := configEnv(t, "keys:\n  dif: D\n")
	km, warn = ConfigKeyMap(env.Getenv, "")
	cfg := filepath.Join(env.Get("XDG_CONFIG_HOME"), "agentctl", "config.yaml")
	if km != nil || warn != `config.yaml keys: unknown action "dif"; using default keys (`+cfg+`)` {
		t.Fatalf("unknown action = %v %q", km, warn)
	}
	_, warn = ConfigKeyMap(configEnv(t, "keys:\n  diff: l\n").Getenv, "earlier")
	if !strings.HasPrefix(warn, `earlier; config.yaml keys: "l" is bound to both diff and logs; using default keys`) {
		t.Fatalf("joined warning = %q", warn)
	}

	// malformed YAML: a banner, defaults
	env = configEnv(t, "keys:\n  diff: [d\n")
	km, warn = ConfigKeyMap(env.Getenv, "")
	if km != nil || !strings.HasPrefix(warn, "config.yaml: ") || !strings.HasSuffix(warn, "; using default keys ("+filepath.Join(env.Get("XDG_CONFIG_HOME"), "agentctl", "config.yaml")+")") || strings.Contains(warn, "\n") {
		t.Fatalf("malformed = %v %q", km, warn)
	}
}

// TestConfigKeysDriveTheTUI: an override changes what the keys do and what
// the footer shows; a malformed file is a banner and the defaults work.
func TestConfigKeysDriveTheTUI(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	km, warn := ConfigKeyMap(configEnv(t, "keys:\n  diff: D\n  close: X\n").Getenv, "")
	if warn != "" {
		t.Fatal(warn)
	}
	m := f.model(t, 120, 40, Options{Keys: km})
	if !strings.Contains(screen(m)[len(screen(m))-1], "D diff") {
		t.Fatalf("footer does not show the override:\n%s", screenText(m))
	}
	if m2 := press(t, m, "d"); len(m2.views) != 0 {
		t.Fatal("the default d still opens the diff view")
	}
	if m2 := press(t, m, "D"); len(m2.views) != 1 {
		t.Fatal("D does not open the diff view")
	}
	if m2 := press(t, m, "X"); len(m2.views) != 1 || !strings.Contains(screenText(m2), "close legacy-pane? y/N") {
		t.Fatalf("X does not ask to close:\n%s", screenText(m2))
	}

	km, warn = ConfigKeyMap(configEnv(t, "keys: [oops\n").Getenv, "")
	m = f.model(t, 120, 40, Options{Keys: km, Warning: warn})
	if got := strings.TrimSpace(screen(m)[2]); !strings.HasPrefix(got, "! config.yaml: ") {
		t.Fatalf("banner = %q\n%s", got, screenText(m))
	}
	if m2 := press(t, m, "d"); len(m2.views) != 1 {
		t.Fatal("defaults do not work after a malformed config")
	}
}
