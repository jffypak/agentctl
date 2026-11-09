package e2e

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// demoScript is scripts/demo.sh in this checkout.
func demoScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the checkout")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "scripts", "demo.sh")
}

// demoWait bounds the whole demo run (it waits on conditions inside; a
// healthy run takes a few seconds).
const demoWait = 3 * time.Minute

// TestDemoScriptFakeagentIsHermetic runs `DEMO_AGENT=fakeagent
// scripts/demo.sh` exactly as documented, on an isolated tmux socket and in a
// hermetic environment, and checks what it claims: two agents in two
// worktrees, both prompts acknowledged, a diff, a TUI run that makes
// `agentctl stats` print a median, close --stop for alpha only, worktrees and
// branches kept, and its own tmux server gone at exit.
func TestDemoScriptFakeagentIsHermetic(t *testing.T) {
	t.Parallel()
	testenv.RequireTool(t, "bash")
	testenv.RequireTool(t, "git")
	bin, fake := testenv.BuildAgentctl(t), testenv.BuildFakeAgent(t)
	// The demo's socket is an empty testenv server: the script reuses an
	// empty server and kills it at exit; testenv's cleanup is the backstop.
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	dir := filepath.Join(t.TempDir(), "demo")
	env := srv.Env.With("DEMO_AGENT=fakeagent", "DEMO_SOCKET="+srv.Name, "DEMO_PAUSE=0", "DEMO_DIR="+dir,
		"AGENTCTL_BIN="+bin, "FAKEAGENT_BIN="+fake)

	ctx, cancel := context.WithTimeout(context.Background(), demoWait)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "bash", demoScript(t))
	cmd.Env, cmd.Dir, cmd.Stdout, cmd.Stderr = env.Strings(), t.TempDir(), &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("demo.sh: %v\n%s", err, out.String())
	}
	o := out.String()
	for _, want := range []string{
		"demo: alpha: ready", "demo: beta: ready", "demo: alpha: both prompts acknowledged",
		"NOTES.md", "open → first action (median)", "FINDINGS (fakeagent):\n  none", "demo: OK",
	} {
		if !strings.Contains(o, want) {
			t.Fatalf("demo output lacks %q:\n%s", want, o)
		}
	}
	if m := regexp.MustCompile(`open → first action \(median\)\s+(\S+)`).FindStringSubmatch(o); m == nil || m[1] == "—" {
		t.Fatalf("stats printed no median:\n%s", o)
	}
	if !regexp.MustCompile(`send #2: #2 queued`).MatchString(o) {
		t.Fatalf("the follow-up prompt was not queued behind the busy turn:\n%s", o)
	}

	// the registry the demo left behind
	p := paths.ForHome(filepath.Join(dir, "agentctl-home"))
	st, err := store.Open(context.Background(), p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, task := range []string{"alpha", "beta"} {
		tk, err := st.Tasks().Get(context.Background(), task)
		if err != nil || tk.State != domain.TaskClosed {
			t.Fatalf("%s: %+v %v", task, tk, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "demo-repo-worktrees", task, "README.md")); err != nil {
			t.Fatalf("%s's worktree: %v", task, err)
		}
	}
	ms, err := st.Messages().ForTask(context.Background(), "alpha", 0)
	if err != nil || len(ms) != 2 || ms[0].State != domain.MsgAcknowledged || ms[1].State != domain.MsgAcknowledged {
		t.Fatalf("alpha's messages: %s %v", describeMessages(ms), err)
	}
	if n, _ := st.Events().CountType(context.Background(), domain.EventUIOpened); n != 1 {
		t.Fatalf("ui.opened = %d", n)
	}
	// the script killed its own server and nothing else
	if _, err := srv.TryRun("list-sessions"); !errors.Is(err, tmux.ErrNoServer) {
		t.Fatalf("the demo's tmux server is still running: %v", err)
	}
}

// TestDemoScriptOnlyTalksToItsOwnSocket — invariant 8 for the demo: every
// tmux invocation in scripts/demo.sh names the demo socket (-L), so neither
// run mode can reach the user's default server.
func TestDemoScriptOnlyTalksToItsOwnSocket(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(demoScript(t))
	if err != nil {
		t.Fatal(err)
	}
	word := regexp.MustCompile(`\btmux\b`)
	n := 0
	for i, line := range strings.Split(string(b), "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") {
			continue
		}
		for _, loc := range word.FindAllStringIndex(code, -1) {
			rest := code[loc[1]:]
			switch {
			case strings.HasPrefix(rest, ` -L "${DEMO_SOCKET}"`), strings.HasPrefix(rest, ` -L ${DEMO_SOCKET}`):
				n++
			case strings.HasPrefix(code, "need tmux "), strings.HasPrefix(rest, "-"): // the PATH check; "tmux-binding"
			default:
				t.Errorf("demo.sh:%d: tmux without -L ${DEMO_SOCKET}: %s", i+1, line)
			}
		}
	}
	if n == 0 {
		t.Fatal("found no tmux invocation at all")
	}
	if !strings.Contains(string(b), `tm() { tmux -L "${DEMO_SOCKET}" -f /dev/null "$@"; }`) {
		t.Fatal("the tm wrapper changed")
	}
}
