package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mod = "example.com/m"

func ruleByID(t *testing.T, id string) Rule {
	t.Helper()
	for _, r := range Rules() {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no rule %q", id)
	return Rule{}
}

func violations(t *testing.T, rule Rule, files map[string]string) []Violation {
	t.Helper()
	m, err := LoadSources(mod, files)
	if err != nil {
		t.Fatal(err)
	}
	return rule.Check(m)
}

// selfTest asserts that every bad fixture violates the rule and every good
// fixture passes it.
type selfTest struct {
	rule string
	bad  map[string]map[string]string
	good map[string]map[string]string
}

func (s selfTest) run(t *testing.T) {
	r := ruleByID(t, s.rule)
	for name, files := range s.bad {
		if v := violations(t, r, files); len(v) == 0 {
			t.Errorf("%s: negative self-test %q produced no violation", s.rule, name)
		} else {
			for _, x := range v {
				if x.Rule != s.rule || x.Pos == "" || x.Msg == "" {
					t.Errorf("%s: malformed violation %+v", s.rule, x)
				}
			}
		}
	}
	for name, files := range s.good {
		if v := violations(t, r, files); len(v) != 0 {
			t.Errorf("%s: allowed case %q produced %v", s.rule, name, v)
		}
	}
}

func TestEveryRuleHasASelfTest(t *testing.T) {
	covered := map[string]bool{}
	for _, s := range selfTests() {
		covered[s.rule] = true
		if len(s.bad) == 0 {
			t.Errorf("rule %s has no negative self-test", s.rule)
		}
	}
	for _, r := range Rules() {
		if !covered[r.ID] {
			t.Errorf("rule %s has no self-test", r.ID)
		}
		if r.Doc == "" {
			t.Errorf("rule %s has no doc", r.ID)
		}
	}
	if len(Rules()) != 7 {
		t.Errorf("rules = %d, want the 7 rules of ARCHITECTURE.md §Code conventions 6", len(Rules()))
	}
}

func TestRuleSelfTests(t *testing.T) {
	for _, s := range selfTests() {
		t.Run(s.rule, s.run)
	}
}

func selfTests() []selfTest {
	return []selfTest{
		{
			rule: "domain-imports",
			bad: map[string]map[string]string{
				"os":             {"internal/domain/x.go": "package domain\nimport _ \"os\"\n"},
				"net/http":       {"internal/domain/x.go": "package domain\nimport _ \"net/http\"\n"},
				"database/sql":   {"internal/domain/sub/x.go": "package sub\nimport _ \"database/sql\"\n"},
				"module package": {"internal/domain/x.go": "package domain\nimport _ \"example.com/m/internal/store\"\n"},
			},
			good: map[string]map[string]string{
				"allowlist":   {"internal/domain/x.go": "package domain\nimport (\n_ \"strings\"\n_ \"time\"\n_ \"unicode/utf8\"\n)\n"},
				"test file":   {"internal/domain/x_test.go": "package domain\nimport _ \"testing\"\n"},
				"not domain":  {"internal/store/x.go": "package store\nimport _ \"os\"\n"},
				"no packages": {},
			},
		},
		{
			rule: "tui-imports",
			bad: map[string]map[string]string{
				"store":        {"internal/tui/x.go": "package tui\nimport _ \"example.com/m/internal/store\"\n"},
				"tmux":         {"internal/tui/x.go": "package tui\nimport _ \"example.com/m/internal/tmux\"\n"},
				"gitx":         {"internal/tui/x.go": "package tui\nimport _ \"example.com/m/internal/gitx\"\n"},
				"hookingest":   {"internal/tui/x.go": "package tui\nimport _ \"example.com/m/internal/hookingest\"\n"},
				"database/sql": {"internal/tui/x.go": "package tui\nimport _ \"database/sql\"\n"},
				"sqlite":       {"internal/tui/x.go": "package tui\nimport _ \"modernc.org/sqlite\"\n"},
				"sqlite lib":   {"internal/tui/views/x.go": "package views\nimport _ \"modernc.org/sqlite/lib\"\n"},
			},
			good: map[string]map[string]string{
				"controller":    {"internal/tui/x.go": "package tui\nimport _ \"example.com/m/internal/controller\"\n"},
				"similar name":  {"internal/tui/x.go": "package tui\nimport _ \"example.com/m/internal/storefront\"\n"},
				"test seeding":  {"internal/tui/x_test.go": "package tui\nimport _ \"example.com/m/internal/store\"\n"},
				"no tui at all": {"internal/store/x.go": "package store\nimport _ \"database/sql\"\n"},
			},
		},
		{
			rule: "controller-hookingest",
			bad: map[string]map[string]string{
				"controller->hookingest": {"internal/controller/x.go": "package controller\nimport _ \"example.com/m/internal/hookingest\"\n"},
				"hookingest->controller": {"internal/hookingest/x.go": "package hookingest\nimport _ \"example.com/m/internal/controller\"\n"},
				"in a test":              {"internal/hookingest/x_test.go": "package hookingest\nimport _ \"example.com/m/internal/controller\"\n"},
			},
			good: map[string]map[string]string{
				"store from both": {
					"internal/controller/x.go": "package controller\nimport _ \"example.com/m/internal/store\"\n",
					"internal/hookingest/x.go": "package hookingest\nimport _ \"example.com/m/internal/store\"\n",
				},
			},
		},
		{
			rule: "exec-ownership",
			bad: map[string]map[string]string{
				"tmux in controller":  {"internal/controller/x.go": "package controller\nimport \"os/exec\"\nvar _ = exec.Command(\"tmux\", \"ls\")\n"},
				"tmux ctx in tui":     {"internal/tui/x.go": "package tui\nimport (\"context\"; \"os/exec\")\nvar _ = exec.CommandContext(context.TODO(), \"tmux\")\n"},
				"tmux abs path":       {"cmd/a/x.go": "package main\nimport \"os/exec\"\nvar _ = exec.Command(\"/usr/bin/tmux\")\n"},
				"aliased exec":        {"internal/store/x.go": "package store\nimport ex \"os/exec\"\nvar _ = ex.Command(\"tmux\")\n"},
				"git in controller":   {"internal/controller/x.go": "package controller\nimport \"os/exec\"\nvar _ = exec.Command(\"git\", \"status\")\n"},
				"tmux in a test file": {"internal/store/x_test.go": "package store\nimport \"os/exec\"\nvar _ = exec.Command(\"tmux\")\n"},
			},
			good: map[string]map[string]string{
				"tmux in tmux":    {"internal/tmux/x.go": "package tmux\nimport \"os/exec\"\nvar _ = exec.Command(\"tmux\")\n"},
				"git in gitx":     {"internal/gitx/x.go": "package gitx\nimport \"os/exec\"\nvar _ = exec.Command(\"git\")\n"},
				"git in testenv":  {"internal/testenv/x.go": "package testenv\nimport \"os/exec\"\nvar _ = exec.Command(\"git\")\n"},
				"other binary":    {"internal/controller/x.go": "package controller\nimport \"os/exec\"\nvar _ = exec.Command(\"less\")\n"},
				"not os/exec":     {"internal/controller/x.go": "package controller\ntype e struct{}\nfunc (e) Command(string) {}\nvar exec e\nfunc f() { exec.Command(\"tmux\") }\n"},
				"tmux subpackage": {"internal/tmux/sub/x.go": "package sub\nimport \"os/exec\"\nvar _ = exec.Command(\"tmux\")\n"},
			},
		},
		{
			rule: "paste-sendkeys",
			bad: map[string]map[string]string{
				"literal in controller":   {"internal/controller/x.go": "package controller\nvar _ = []string{\"paste-buffer\", \"-p\"}\n"},
				"send-keys in claude":     {"internal/adapter/claude/observe.go": "package claude\nvar _ = []string{\"send-keys\", \"Enter\"}\n"},
				"alias pasteb":            {"internal/tmux/capture.go": "package tmux\nvar _ = \"pasteb -t %1\"\n"},
				"literal even in deliver": {"internal/adapter/claude/deliver.go": "package claude\nvar _ = \"send-keys\"\n"},
				"builder in controller":   {"internal/controller/x.go": "package controller\nimport \"example.com/m/internal/tmux\"\nvar _ = tmux.PasteBufferCommand\n"},
				"builder in tui":          {"internal/tui/x.go": "package tui\nfunc f(c interface{ SendKeys(string) }) { c.SendKeys(\"x\") }\n"},
				"builder in tmux file":    {"internal/tmux/capture.go": "package tmux\nfunc f() { sendKeysArgs() }\n"},
			},
			good: map[string]map[string]string{
				"builders in buffer.go":  {"internal/tmux/buffer.go": "package tmux\nfunc PasteBufferCommand() []string { return []string{\"paste-buffer\", \"-p\", \"-d\"} }\nfunc SendKeysCommand() []string { return []string{\"send-keys\", \"Enter\"} }\n"},
				"used from deliver.go":   {"internal/adapter/claude/deliver.go": "package claude\nimport \"example.com/m/internal/tmux\"\nvar _ = tmux.PasteBufferCommand\n"},
				"tests may type":         {"internal/hookingest/x_test.go": "package hookingest\nvar _ = []string{\"send-keys\", \"y\"}\n"},
				"testenv":                {"internal/testenv/tuidriver.go": "package testenv\nvar _ = []string{\"send-keys\", \"q\"}\n"},
				"e2e":                    {"internal/e2e/x.go": "package e2e\nvar _ = \"paste-buffer\"\n"},
				"unrelated send command": {"cmd/agentctl/commands.go": "package main\nvar _ = \"send\"\n"},
			},
		},
		{
			rule: "kill-commands",
			bad: map[string]map[string]string{
				"kill-session in controller": {"internal/controller/x.go": "package controller\nvar _ = []string{\"kill-session\", \"-t\"}\n"},
				"kill-pane in tui":           {"internal/tui/x.go": "package tui\nvar _ = \"kill-pane\"\n"},
				"killp alias":                {"internal/tmux/attach.go": "package tmux\nvar _ = \"killp\"\n"},
				"kill-server in tmux":        {"internal/tmux/x.go": "package tmux\nvar _ = \"kill-server\"\n"},
				"kill-server in a test":      {"internal/controller/x_test.go": "package controller\nvar _ = \"kill-server\"\n"},
				"kill-pane in a test":        {"internal/e2e/x_test.go": "package e2e\nvar _ = \"kill-pane\"\n"},
			},
			good: map[string]map[string]string{
				"kill.go":            {"internal/tmux/kill.go": "package tmux\nvar _ = []string{\"kill-session\", \"kill-pane\"}\n"},
				"stop.go":            {"internal/adapter/claude/stop.go": "package claude\nvar _ = \"kill-session\"\n"},
				"kill-server in env": {"internal/testenv/tmux.go": "package testenv\nvar _ = \"kill-server\"\n"},
			},
		},
		{
			rule: "gitx-nondestructive",
			bad: map[string]map[string]string{
				"reset --hard":    {"internal/gitx/x.go": "package gitx\nvar _ = []string{\"reset\", \"--hard\"}\n"},
				"worktree remove": {"internal/gitx/x.go": "package gitx\nvar _ = \"worktree remove\"\n"},
				"branch -D":       {"internal/gitx/x.go": "package gitx\nvar _ = []string{\"branch\", \"-D\", \"x\"}\n"},
				"--force":         {"internal/gitx/x.go": "package gitx\nvar _ = \"--force\"\n"},
				"force-with":      {"internal/gitx/x.go": "package gitx\nvar _ = \"--force-with-lease\"\n"},
				"clean -f":        {"internal/gitx/x.go": "package gitx\nvar _ = []string{\"clean\", \"-f\"}\n"},
				"push":            {"internal/gitx/x.go": "package gitx\nvar _ = \"push\"\n"},
				"in gitx test":    {"internal/gitx/x_test.go": "package gitx\nvar _ = \"push\"\n"},
			},
			good: map[string]map[string]string{
				"reads":        {"internal/gitx/x.go": "package gitx\nvar _ = []string{\"diff\", \"--numstat\", \"-z\", \"worktree\", \"add\", \"-b\"}\n"},
				"outside gitx": {"internal/testenv/x.go": "package testenv\nvar _ = []string{\"branch\", \"-D\", \"x\"}\n"},
			},
		},
	}
}

func moduleRoot(t *testing.T) (string, string) {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "module ") {
					return dir, strings.TrimSpace(strings.TrimPrefix(line, "module "))
				}
			}
			t.Fatal("go.mod has no module line")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// TestRepositoryConformsToArchitecture is the real gate.
func TestRepositoryConformsToArchitecture(t *testing.T) {
	root, modPath := moduleRoot(t)
	m, err := Load(root, modPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) < 20 {
		t.Fatalf("loaded only %d files from %s", len(m.Files), root)
	}
	for _, f := range m.Files {
		if f.in(selfDir) {
			t.Fatalf("archtest scanned itself: %s", f.Rel)
		}
	}
	for _, v := range Check(m, Rules()) {
		t.Error(v)
	}
}
