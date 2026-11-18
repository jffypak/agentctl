package hookingest

import (
	"os"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/taskctx"
)

// TestSessionStartReturnsTaskContext: every Claude SessionStart (startup,
// resume, clear, compact) returns the task's context text for stdout, and
// creates the file the first time.
func TestSessionStartReturnsTaskContext(t *testing.T) {
	for _, source := range []string{"startup", "resume", "clear", "compact"} {
		t.Run(source, func(t *testing.T) {
			f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
			r := f.run(f.env("demo"), payload("SessionStart", "source", source))
			if r.Err != nil {
				t.Fatal(r.Err)
			}
			path := taskctx.Path(f.p.Home, "demo")
			if !strings.Contains(r.Context, path) {
				t.Fatalf("Context lacks %s:\n%s", path, r.Context)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("context file not created: %v", err)
			}
		})
	}
}

func TestOtherEventsReturnNoContext(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateWorking)
	for _, ev := range []string{"UserPromptSubmit", "Stop", "PreToolUse"} {
		if r := f.run(f.env("demo"), payload(ev, "prompt", "hi", "tool_name", "Bash")); r.Context != "" {
			t.Errorf("%s returned context:\n%s", ev, r.Context)
		}
	}
}

func TestUnmanagedPaneGetsNoContext(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateStarting)
	env := f.env("")
	env["TMUX_PANE"] = "%99"
	if r := f.run(env, payload("SessionStart", "source", "startup")); r.Context != "" {
		t.Fatalf("unmanaged pane got context:\n%s", r.Context)
	}
}
