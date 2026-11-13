package claude_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/testenv"
)

const goldenAgentctl = "/opt/agentctl/bin/agentctl"

type settingsDoc struct {
	Hooks map[string][]struct {
		Matcher *string `json:"matcher"`
		Hooks   []struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// TestEnsureSettingsGolden pins the generated file byte for byte: every mapped
// event, one command hook each, shell-quoted absolute path + " hook",
// timeout 5, mode 0600, and a rewrite that is byte-identical.
func TestEnsureSettingsGolden(t *testing.T) {
	home := testenv.TempHome(t)
	a := claude.New(claude.Config{AgentctlPath: goldenAgentctl})
	path, err := a.EnsureSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	if path != claude.SettingsPath(home) {
		t.Fatalf("path = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "hooks", "settings.golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("generated settings differ from testdata/hooks/settings.golden.json:\n%s", got)
	}
	assertMode(t, path, 0o600)
	assertMode(t, filepath.Dir(path), 0o700)

	var doc settingsDoc
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Hooks) != len(claude.HookEvents()) {
		t.Fatalf("events = %d, want %d", len(doc.Hooks), len(claude.HookEvents()))
	}
	for _, ev := range claude.HookEvents() {
		groups := doc.Hooks[ev]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || groups[0].Matcher != nil {
			t.Fatalf("%s: want one matcher-less group with one hook, got %+v", ev, groups)
		}
		h := groups[0].Hooks[0]
		if h.Type != "command" || h.Command != "'"+goldenAgentctl+"' hook" || h.Timeout != 5 {
			t.Fatalf("%s: hook = %+v", ev, h)
		}
	}

	before, _ := os.Stat(path)
	again, err := a.EnsureSettings(home)
	if err != nil || again != path {
		t.Fatalf("second EnsureSettings = %q, %v", again, err)
	}
	rewritten, _ := os.ReadFile(path)
	if string(rewritten) != string(got) {
		t.Fatal("rewrite is not byte-identical")
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("unchanged settings were rewritten (inode changed)")
	}
	assertNoTempFiles(t, filepath.Dir(path))
}

// TestEnsureSettingsMappedEvents pins the event list to ARCHITECTURE.md's
// hook->state table (every row that can change state).
func TestEnsureSettingsMappedEvents(t *testing.T) {
	want := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PreCompact",
		"Notification", "PermissionRequest", "Stop", "StopFailure", "SessionEnd"}
	if got := claude.HookEvents(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("HookEvents = %v, want %v", got, want)
	}
}

func TestEnsureSettingsReplacesStaleFileAtomically(t *testing.T) {
	home := testenv.TempHome(t)
	path := claude.SettingsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := claude.New(claude.Config{AgentctlPath: "/new/agentctl"}).EnsureSettings(home); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"'/new/agentctl' hook"`) {
		t.Fatalf("stale settings not replaced:\n%s", b)
	}
	assertMode(t, path, 0o600)
	assertNoTempFiles(t, filepath.Dir(path))

	// a different agentctl path regenerates; the same path is a no-op
	if _, err := claude.New(claude.Config{AgentctlPath: "/other/agentctl"}).EnsureSettings(home); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), `"'/other/agentctl' hook"`) {
		t.Fatalf("settings not regenerated for a new path:\n%s", b)
	}
}

func TestEnsureSettingsRejectsBadInputs(t *testing.T) {
	cases := map[string]struct{ exe, home string }{
		"no agentctl path":       {"", testenv.TempHome(t)},
		"relative agentctl path": {"bin/agentctl", testenv.TempHome(t)},
		"relative home":          {goldenAgentctl, "home"},
		"newline in path":        {"/opt/a\nb/agentctl", testenv.TempHome(t)},
	}
	for name, c := range cases {
		if _, err := claude.New(claude.Config{AgentctlPath: c.exe}).EnsureSettings(c.home); err == nil || strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: err = %v, want a one-line error", name, err)
		}
	}
}

func TestHookCommandShellQuotes(t *testing.T) {
	cases := map[string]string{
		"/opt/agentctl/bin/agentctl":  `'/opt/agentctl/bin/agentctl' hook`,
		"/Users/me/My Tools/agentctl": `'/Users/me/My Tools/agentctl' hook`,
		"/tmp/it's/agentctl":          `'/tmp/it'\''s/agentctl' hook`,
		"/tmp/$(rm -rf x)/agentctl":   `'/tmp/$(rm -rf x)/agentctl' hook`,
	}
	for in, want := range cases {
		if got := claude.HookCommand(in); got != want {
			t.Errorf("HookCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSettingsJSONIsTheFileContent: hooks-snippet prints SettingsJSON, which
// must be exactly what EnsureSettings writes minus the status line, so
// merging the snippet never replaces a user's own status line.
func TestSettingsJSONIsTheFileContent(t *testing.T) {
	home := testenv.TempHome(t)
	path, err := claude.New(claude.Config{AgentctlPath: goldenAgentctl}).EnsureSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := os.ReadFile(path)
	launch, err := claude.LaunchSettingsJSON(goldenAgentctl)
	if err != nil || string(launch) != string(file) {
		t.Fatalf("LaunchSettingsJSON differs from the file (%v):\n%s", err, launch)
	}
	snippet, err := claude.SettingsJSON(goldenAgentctl)
	if err != nil {
		t.Fatal(err)
	}
	var withLine, without map[string]json.RawMessage
	if json.Unmarshal(file, &withLine) != nil || json.Unmarshal(snippet, &without) != nil {
		t.Fatal("settings are not JSON objects")
	}
	if _, ok := without["statusLine"]; ok {
		t.Fatalf("snippet has a statusLine:\n%s", snippet)
	}
	if string(withLine["hooks"]) != string(without["hooks"]) || len(withLine) != 2 {
		t.Fatalf("snippet hooks differ from the file:\n%s", snippet)
	}
	var sl struct{ Type, Command string }
	if err := json.Unmarshal(withLine["statusLine"], &sl); err != nil || sl.Type != "command" || sl.Command != "'"+goldenAgentctl+"' statusline" {
		t.Fatalf("statusLine = %+v (%v)", sl, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, fi.Mode().Perm(), want)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "claude-settings.json" {
			t.Fatalf("leftover file %s in %s", e.Name(), dir)
		}
	}
}
