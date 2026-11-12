package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewAgentCodexFlag(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	b, _ := json.Marshal([]string{"sh", "-c", "exec sleep 600", "codex"})
	env := f.env.With("AGENTCTL_CODEX_CMD=" + string(b))
	if r := runCmd(t, env, f.repo.Root, "new", "cx", "--agent", "codex", "--goal", "g"); r.code != 0 || !strings.HasPrefix(r.stdout, "started cx · ") {
		t.Fatalf("new --agent codex = %+v", r)
	}
	if r := runCmd(t, env, f.repo.Root, "new", "--agent=claude", "cl", "--goal", "g"); r.code != 0 {
		t.Fatalf("new --agent=claude = %+v", r)
	}
	tasks := lsTasks(t, env, f.repo.Root)
	if got := tasks["cx"]["session"].(map[string]any)["agent_type"]; got != "codex" {
		t.Fatalf("cx agent_type = %v", got)
	}
	if got := tasks["cl"]["session"].(map[string]any)["agent_type"]; got != "claude" {
		t.Fatalf("cl agent_type = %v", got)
	}
	r := runCmd(t, env, f.repo.Root, "new", "bad", "--agent", "gemini", "--goal", "g")
	if r.code != 1 || strings.Count(r.stderr, "\n") != 1 || !strings.Contains(r.stderr, `unknown agent "gemini"`) || r.stdout != "" {
		t.Fatalf("new --agent gemini = %+v", r)
	}
	if _, ok := lsTasks(t, env, f.repo.Root)["bad"]; ok {
		t.Fatal("a task was written for an unknown agent")
	}
}
