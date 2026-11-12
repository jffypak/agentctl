package main

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/testenv"
)

func TestParseCodexArgs(t *testing.T) {
	cases := []struct {
		in   []string
		name string
		rest []string
	}{
		{nil, "", nil},
		{[]string{"--name", "w"}, "w", nil},
		{[]string{"--name=w", "--model", "x"}, "w", []string{"--model", "x"}},
		{[]string{"--", "--name", "x"}, "", []string{"--name", "x"}},
		{[]string{"--name", "w", "--", "-c", "a=b"}, "w", []string{"-c", "a=b"}},
		{[]string{"resume", "--last"}, "", []string{"resume", "--last"}},
	}
	for _, c := range cases {
		name, rest, err := parseCodexArgs(c.in)
		if err != nil || name != c.name || !reflect.DeepEqual(rest, c.rest) {
			t.Errorf("parseCodexArgs(%q) = %q %q %v, want %q %q", c.in, name, rest, err, c.name, c.rest)
		}
	}
	if _, _, err := parseCodexArgs([]string{"--name"}); err == nil {
		t.Error("dangling --name accepted")
	}
}

// Review Focus 2: outside tmux, agentctl codex fails with one line and
// registers nothing.
func TestCmdCodexOutsideTmux(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	env := f.env.With("TMUX=", "TMUX_PANE=")
	r := runCmd(t, env, f.repo.Root, "codex", "--name", "w")
	if r.code != 1 || r.stdout != "" || strings.Count(r.stderr, "\n") != 1 || !strings.Contains(r.stderr, "must run inside tmux") {
		t.Fatalf("codex outside tmux = %+v", r)
	}
	if n := rowCounts(t, env); n != [3]int{} {
		t.Fatalf("rows written: %v", n)
	}
}

// TestCmdCodexEndToEnd: in a real pane, agentctl codex registers the pane,
// runs Codex (fakeagent --codex) as its child with agentctl's hooks, the
// hooks drive the state, and Codex's exit is recorded while the pane (the
// user's shell) lives on. A second agentctl codex in the same pane reuses
// the task.
func TestCmdCodexEndToEnd(t *testing.T) {
	t.Parallel()
	it := newPromptIT(t, true)
	codexCmd, _ := json.Marshal([]string{"/usr/bin/env", "FAKEAGENT_WORK_MS=200",
		"FAKEAGENT_LOG=" + filepath.Join(it.logs, "codex-input.jsonl"), it.fake, "--codex"})
	created := it.srv.Run(t, "new-session", "-d", "-P", "-F", "#{pane_id}", "-s", "user", "-c", it.repo.Root, "/bin/sh")
	pane := strings.TrimSpace(created)
	// The pane inherits the hermetic base env, whose AGENTCTL_TMUX_SOCKET names
	// no server; point it at this test server (real use: unset, $TMUX wins).
	prefix := "AGENTCTL_TMUX_SOCKET=" + it.srv.Socket + " AGENTCTL_CODEX_CMD='" + string(codexCmd) + "' "
	it.keys(pane, prefix+it.bin+" codex --name w", true)
	testenv.WaitFor(t, 10*time.Second, "task w registered", func() (bool, string) {
		out, _, _ := it.agentctl("ls", "--json")
		var doc struct {
			Tasks []struct {
				ID string `json:"id"`
			} `json:"tasks"`
		}
		_ = json.Unmarshal([]byte(out), &doc)
		scr, _ := it.srv.TryRun("capture-pane", "-p", "-t", pane)
		for _, tk := range doc.Tasks {
			if tk.ID == "w" {
				return true, scr
			}
		}
		return false, scr
	})
	tk := it.waitState("w", "starting", 10*time.Second)
	if tk.Section != "initial" || tk.Session.Origin != "wrapped" || tk.Session.AgentType != "codex" {
		t.Fatalf("registered task = %+v", tk)
	}
	testenv.WaitFor(t, 0, "codex prompt", func() (bool, string) {
		out, _ := it.srv.TryRun("capture-pane", "-p", "-t", pane)
		return strings.HasSuffix(strings.TrimSpace(out), ">"), out
	})
	it.keys(pane, "hello", true)
	it.waitState("w", "ready", 10*time.Second)

	it.keys(pane, "#exit 0", true)
	done := it.waitState("w", "stopped", 10*time.Second)
	if !strings.Contains(done.LastDetail, "codex exited (code 0)") {
		t.Fatalf("exit detail = %q", done.LastDetail)
	}
	if dead := it.srv.Run(t, "display-message", "-p", "-t", pane, "#{pane_dead}"); dead != "0" {
		t.Fatalf("the user's pane died with Codex (pane_dead %q)", dead)
	}

	it.keys(pane, prefix+it.bin+" codex", true)
	again := it.waitState("w", "starting", 10*time.Second)
	if again.Session.ID == tk.Session.ID {
		t.Fatalf("rerun did not start a new session: %+v", again)
	}
}
