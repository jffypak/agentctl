package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// noisyAgentCmd prints terminal escapes (OSC 52 clipboard write, OSC title,
// CSI color and cursor moves) and 60 numbered lines separated by blank
// lines, then idles.
var noisyAgentCmd = []string{"sh", "-c", `printf 'boot\033]52;c;ZXZpbA==\007 ok\n\033]0;title\007\033[31mred\033[0m\033[2;1H\n'; ` +
	`i=1; while [ $i -le 60 ]; do printf 'line %d\n\n' $i; i=$((i+1)); done; echo READY; exec sleep 600`, "agent"}

func newAgentFixture(t *testing.T, agent []string) *cliFixture {
	t.Helper()
	f := newCLIFixture(t)
	b, _ := json.Marshal(agent)
	f.env = f.env.With("AGENTCTL_AGENT_CMD=" + string(b))
	if r := runCmd(t, f.env, f.repo.Root, "new", "demo", "--goal", "g"); r.code != 0 {
		t.Fatalf("new = %+v", r)
	}
	return f
}

func waitForShow(t *testing.T, f *cliFixture, want string) result {
	t.Helper()
	var r result
	testenv.WaitFor(t, 0, "show output "+want, func() (bool, string) {
		r = runCmd(t, f.env, f.repo.Root, "show", "demo")
		return r.code == 0 && strings.Contains(r.stdout, want), r.stdout + r.stderr
	})
	return r
}

// section returns the lines after the line starting with header, up to the
// next blank line.
func section(out, header string) []string {
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, header) {
			var body []string
			for _, b := range lines[i+1:] {
				if b == "" {
					break
				}
				body = append(body, b)
			}
			return body
		}
	}
	return nil
}

func TestShowPrintsStateBlockAndLast40Lines(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t, noisyAgentCmd)
	r := waitForShow(t, f, "READY")
	if r.stderr != "" {
		t.Fatalf("stderr = %q", r.stderr)
	}
	wt := filepath.Join(f.repo.WorktreesDir(), "demo")
	for _, want := range []string{
		"task       demo · INITIALSTATE · no events yet",
		"state      starting · confidence none",
		"branch     jpark/demo · base main @ ",
		"worktree   " + wt,
		"session    #1 claude · launched · tmux jpark-demo",
		"identity   socket " + f.srv.Socket + " · server pid ",
		" · pane %0 · pane pid ",
		"pane       alive",
		"diff vs base ",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("show is missing %q:\n%s", want, r.stdout)
		}
	}
	out := section(r.stdout, "recent output")
	if len(out) != 40 || strings.TrimSpace(out[0]) != "line 22" || strings.TrimSpace(out[39]) != "READY" {
		t.Fatalf("recent output = %d lines %q", len(out), out)
	}
	for _, l := range out {
		if strings.TrimSpace(l) == "" {
			t.Fatalf("empty line in recent output: %q", out)
		}
	}
	if strings.ContainsAny(r.stdout, "\x1b\x07\r") || strings.Contains(r.stdout, "52;c") || strings.Contains(r.stdout, "title") {
		t.Fatalf("show leaked terminal escapes: %q", r.stdout)
	}
}

func TestShowFullWritesPrivateLogAndPrintsPath(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t, noisyAgentCmd)
	waitForShow(t, f, "READY")
	r := runCmd(t, f.env, f.repo.Root, "show", "demo", "--full")
	if r.code != 0 || r.stderr != "" || strings.Count(r.stdout, "\n") != 1 {
		t.Fatalf("show --full = %+v", r)
	}
	path := strings.TrimSuffix(r.stdout, "\n")
	dir := filepath.Join(f.env.Get("AGENTCTL_HOME"), "logs", "demo")
	if filepath.Dir(path) != dir || !regexp.MustCompile(`^\d{8}T\d{6}\.\d{3}Z\.log$`).MatchString(filepath.Base(path)) {
		t.Fatalf("path = %q, want %s/<UTC>.log", path, dir)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, dir: 0o700} {
		fi, err := os.Stat(p)
		if err != nil || fi.Mode().Perm() != want {
			t.Fatalf("%s: %v %v, want mode %v", p, fi, err, want)
		}
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "line 1\n") || !strings.Contains(string(b), "READY") || strings.ContainsAny(string(b), "\x1b\x07") {
		t.Fatalf("log content = %q", b)
	}
}

func TestShowReusedPaneShowsDiffAndNeverCaptures(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t, []string{"sh", "-c", `echo "demo output"; exec sleep 600`, "agent"})
	waitForShow(t, f, "demo output")
	wt := filepath.Join(f.repo.WorktreesDir(), "demo")
	if err := os.WriteFile(filepath.Join(wt, "work.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f.srv.Restart(t)
	created, err := f.srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: "intruder", Dir: t.TempDir(), Argv: []string{"/bin/sh", "-c", `echo "SECRET of another agent"; exec sleep 600`},
	})
	if err != nil || created.PaneID != "%0" {
		t.Fatalf("intruder = %+v %v (want reused %%0)", created, err)
	}
	testenv.WaitFor(t, 0, "intruder output", func() (bool, string) {
		out := f.srv.Run(t, "capture-pane", "-p", "-t", "%0")
		return strings.Contains(out, "SECRET"), out
	})

	r := runCmd(t, f.env, f.repo.Root, "show", "demo")
	if r.code != 0 || strings.Contains(r.stdout, "SECRET") {
		t.Fatalf("show = %+v", r)
	}
	for _, want := range []string{"pane unavailable (disconnected: identity_mismatch)", "untracked  1 file +1", "work.txt  +1"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("show is missing %q:\n%s", want, r.stdout)
		}
	}
	full := runCmd(t, f.env, f.repo.Root, "show", "demo", "--full")
	if full.code != 1 || full.stdout != "" || !strings.Contains(full.stderr, "pane unavailable (disconnected: identity_mismatch)") ||
		strings.Count(full.stderr, "\n") != 1 {
		t.Fatalf("show --full on a reused pane = %+v", full)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.env.Get("AGENTCTL_HOME"), "logs", "demo")); len(entries) != 0 {
		t.Fatalf("log written for a reused pane: %v", entries)
	}
	d := runCmd(t, f.env, f.repo.Root, "diff", "demo")
	if d.code != 0 || !strings.Contains(d.stdout, "+++ b/work.txt") {
		t.Fatalf("diff of a disconnected task = %+v", d)
	}
}

func TestShowDeadPaneStillShowsFinalOutput(t *testing.T) {
	t.Parallel()
	f := newAgentFixture(t, []string{"sh", "-c", `echo "final words before crash"; exit 3`, "agent"})
	r := waitForShow(t, f, "dead (exit 3)")
	if !strings.Contains(strings.Join(section(r.stdout, "recent output"), "\n"), "final words before crash") {
		t.Fatalf("dead pane show = %s", r.stdout)
	}
}

func TestShowAndDiffUsageAndUnknownTask(t *testing.T) {
	t.Parallel()
	f := newCLIFixture(t)
	for _, args := range [][]string{{"show"}, {"show", "a", "b"}, {"diff"}, {"diff", "a", "b"}, {"show", "--bogus", "a"}, {"diff", "--bogus", "a"}} {
		if r := runCmd(t, f.env, f.repo.Root, args...); r.code != 2 || r.stdout != "" {
			t.Errorf("%q = %+v, want usage exit 2", args, r)
		}
	}
	for _, args := range [][]string{{"show", "nope"}, {"show", "nope", "--full"}, {"diff", "nope"}, {"diff", "--stat", "nope"}} {
		r := runCmd(t, f.env, f.repo.Root, args...)
		if r.code != 1 || r.stdout != "" || strings.Count(r.stderr, "\n") != 1 || !strings.Contains(r.stderr, `task "nope" not found`) {
			t.Errorf("%q = %+v, want one-line not-found exit 1", args, r)
		}
	}
}
