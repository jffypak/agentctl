package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/testenv"
)

// The new-task form has an agent field: tab reaches it, ←/→/space toggles
// claude ⇄ codex, and the choice is passed to NewTask.
func TestNewTaskFormAgentToggle(t *testing.T) {
	t.Parallel()
	codexCmd := "AGENTCTL_CODEX_CMD" + strings.TrimPrefix(fakeAgentCmd, "AGENTCTL_AGENT_CMD")
	f := newManageFixtureEnv(t, testenv.NewEnv(t).With(fakeAgentCmd, codexCmd))
	m := f.managed(t, 120, 40, Options{})
	m = press(t, m, "n")
	if txt := screenText(m); !strings.Contains(txt, "agent  claude") {
		t.Fatalf("agent field missing or not claude by default:\n%s", txt)
	}
	m = typeText(t, m, "cx")
	m = press(t, m, "tab", "tab") // name -> goal -> agent
	m = press(t, m, "right")
	if txt := screenText(m); !strings.Contains(txt, "agent  codex") {
		t.Fatalf("right did not switch to codex:\n%s", txt)
	}
	m = press(t, m, "space", "space") // back to claude, then codex again
	if txt := screenText(m); !strings.Contains(txt, "agent  codex") {
		t.Fatalf("space did not toggle:\n%s", txt)
	}
	m, cmd := pressCmd(t, m, "enter")
	m = runLong(t, m, cmd)
	if topView(m) != nil {
		t.Fatalf("form still open:\n%s", screenText(m))
	}
	ov, err := f.ctl.Overview(context.Background(), controller.OverviewQuery{Cwd: f.repo.Root})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ov.Rows {
		if r.ID == "cx" && r.AgentType != "codex" {
			t.Fatalf("cx agent = %q, want codex", r.AgentType)
		}
	}
}

// With Claude and Codex rows visible, each row names its agent; with one
// agent type, no tag (unchanged screens).
func TestListAgentTagWhenMixed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := f.model(t, 120, 40, Options{})
	if strings.Contains(plain(lineWith(m, "payments")), "claude") {
		t.Fatalf("single-agent list shows a tag: %q", plain(lineWith(m, "payments")))
	}
	f.exec(t, `UPDATE sessions SET agent_type = 'codex' WHERE task_id = 'payments'`)
	m = f.model(t, 120, 40, Options{})
	if row := plain(lineWith(m, "payments")); !strings.Contains(row, "codex") {
		t.Fatalf("codex row has no tag: %q", row)
	}
	if row := plain(lineWith(m, "docs-site")); !strings.Contains(row, "claude") {
		t.Fatalf("claude row has no tag: %q", row)
	}
}

// The adopt picker shows the listing's Codex hint (a normal codex pane can't
// be tracked; agentctl codex is the way).
func TestAdoptPickerShowsCodexHint(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := press(t, f.model(t, 100, 24, Options{}), "a")
	v := topView(m).(adoptView)
	hint := "pane %9 runs codex; agentctl can't track it. Restart it with: agentctl codex"
	cands := controller.AdoptCandidates{Socket: "/s", Commands: []string{"claude"}, Hints: []string{hint}}
	m = update(t, m, adoptListedMsg{id: v.id, seq: v.listSeq, cands: cands})
	if txt := screenText(m); !strings.Contains(txt, hint) {
		t.Fatalf("codex hint not shown:\n%s", txt)
	}
}
