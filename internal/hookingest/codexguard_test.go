package hookingest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
)

// newCodexFixture is newFixture whose session is a Codex session.
func newCodexFixture(t *testing.T, origin domain.Origin, state domain.AgentState) *fixture {
	t.Helper()
	f := newFixture(t, origin, state)
	if _, err := f.st.DB().ExecContext(context.Background(), `UPDATE sessions SET agent_type = 'codex' WHERE id = ?`, f.sess.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

func codexHook(parent string, err error) func(*Config) {
	return func(c *Config) {
		c.Agent = "codex"
		c.ParentCommand = func() (string, error) { return parent, err }
	}
}

const embeddedCodex = "codex -c hooks.Stop=[...]"

func TestIsCodexDaemon(t *testing.T) {
	cases := map[string]bool{
		"/Users/u/.codex/packages/app-server-daemon/releases/0.167.2-aarch64-apple-darwin/bin/codex app-server --listen unix:// --managed-daemon": true,
		"/x/codex app-server daemon pid-update-loop": true,
		"codex app-server":                           true,
		"codex":                                      false,
		"codex --dangerously-bypass-hook-trust":      false,
		embeddedCodex:                                false,
		"/x/codex-code-mode-host":                    false,
		"node app-server":                            false,
		"/bin/sh -c 'codex app-server'":              false,
		"":                                           false,
	}
	for cmd, want := range cases {
		if got := isCodexDaemon(cmd); got != want {
			t.Errorf("isCodexDaemon(%q) = %v, want %v", cmd, got, want)
		}
	}
	if !fromCodexDaemon("/bin/sh -c '/opt/agentctl' hook --agent codex\n/x/codex app-server --listen unix://") {
		t.Error("daemon as grandparent (sh -c did not exec) not detected")
	}
	if fromCodexDaemon("/bin/sh -c x\ncodex -c hooks.Stop=[...]") {
		t.Error("embedded codex as grandparent detected as daemon")
	}
}

// Review Focus 3: agentctl's Codex hook firing inside `codex app-server`
// (e.g. copied into ~/.codex/hooks.json) carries the daemon's $TMUX_PANE,
// which can look valid. It is an orphan and changes nothing.
func TestCodexHookFromDaemonIsOrphan(t *testing.T) {
	f := newCodexFixture(t, domain.OriginLaunched, domain.StateWorking)
	r := f.run(f.env("demo"), payload("Stop"), codexHook("/x/codex app-server --listen unix://", nil))
	if r.Orphan != OrphanCodexDaemon || r.Applied {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	if s := f.session(); s.State != domain.StateWorking {
		t.Fatalf("state = %s, want working (unchanged)", s.State)
	}
	if ev := f.events(domain.EventHookOrphan); len(ev) != 1 || decode(t, ev[0].Payload)["reason"] != OrphanCodexDaemon {
		t.Fatalf("orphan events = %v", ev)
	}
	if len(f.events(domain.EventHookReceived)) != 0 {
		t.Fatal("a daemon hook was recorded as received")
	}
}

func TestCodexHookEmbeddedApplies(t *testing.T) {
	f := newCodexFixture(t, domain.OriginLaunched, domain.StateWorking)
	r := f.run(f.env("demo"), payload("Stop"), codexHook(embeddedCodex, nil))
	if r.Err != nil || r.Orphan != "" || !r.Applied {
		t.Fatalf("outcome = %+v (stderr %s, log %s)", r.Outcome, r.stderr, f.errorLog())
	}
	if s := f.session(); s.State != domain.StateReady || s.LastDetail != "turn finished" {
		t.Fatalf("session = %s %q", s.State, s.LastDetail)
	}
}

// Codex SessionStart is record only: the session stays starting (but has now
// sent its first event).
func TestCodexSessionStartIsRecordOnly(t *testing.T) {
	f := newCodexFixture(t, domain.OriginLaunched, domain.StateStarting)
	r := f.run(f.env("demo"), payload("SessionStart", "source", "startup"), codexHook(embeddedCodex, nil))
	if r.Err != nil || r.Orphan != "" {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	if s := f.session(); s.State != domain.StateStarting || !s.HasHook() {
		t.Fatalf("session = %s hasHook %v", s.State, s.HasHook())
	}
}

// A ParentCommand failure never blocks a hook (fail open).
func TestParentCommandErrorFailsOpen(t *testing.T) {
	f := newCodexFixture(t, domain.OriginLaunched, domain.StateWorking)
	r := f.run(f.env("demo"), payload("Stop"), codexHook("", errors.New("ps: no such process")))
	if !r.Applied || f.session().State != domain.StateReady {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	if !strings.Contains(f.errorLog(), "ps: no such process") {
		t.Errorf("parent lookup failure not logged: %q", f.errorLog())
	}
}

func TestCodexForeignSessionIgnored(t *testing.T) {
	f := newCodexFixture(t, domain.OriginLaunched, domain.StateStarting)
	f.run(f.env("demo"), payload("UserPromptSubmit", "session_id", "A", "prompt", "hi"), codexHook(embeddedCodex, nil))
	if s := f.session(); s.AgentSessionID != "A" || s.State != domain.StateWorking {
		t.Fatalf("after A's prompt: %+v", s)
	}
	r := f.run(f.env("demo"), payload("Stop", "session_id", "B"), codexHook(embeddedCodex, nil))
	if r.Applied || r.Orphan != "" {
		t.Fatalf("foreign Stop outcome = %+v", r.Outcome)
	}
	s := f.session()
	if s.State != domain.StateWorking || s.AgentSessionID != "A" {
		t.Fatalf("foreign Stop changed the session: %s %q", s.State, s.AgentSessionID)
	}
	rec := f.events(domain.EventHookReceived)
	if last := decode(t, rec[len(rec)-1].Payload); last["foreign_session"] != "1" || last["event"] != "Stop" {
		t.Fatalf("last hook.received = %v", last)
	}
	// A's own Stop still applies.
	if r := f.run(f.env("demo"), payload("Stop", "session_id", "A"), codexHook(embeddedCodex, nil)); !r.Applied {
		t.Fatalf("own Stop outcome = %+v", r.Outcome)
	}
}

func TestCodexSessionStartClearRebinds(t *testing.T) {
	f := newCodexFixture(t, domain.OriginLaunched, domain.StateStarting)
	f.run(f.env("demo"), payload("UserPromptSubmit", "session_id", "A"), codexHook(embeddedCodex, nil))
	f.run(f.env("demo"), payload("Stop", "session_id", "A"), codexHook(embeddedCodex, nil))
	for _, src := range []string{"clear", "resume", "fork"} {
		id := "new-" + src
		f.run(f.env("demo"), payload("SessionStart", "session_id", id, "source", src), codexHook(embeddedCodex, nil))
		if s := f.session(); s.AgentSessionID != id {
			t.Fatalf("SessionStart(%s) did not rebind: %q", src, s.AgentSessionID)
		}
	}
	// startup from another id does not rebind
	f.run(f.env("demo"), payload("SessionStart", "session_id", "Z", "source", "startup"), codexHook(embeddedCodex, nil))
	if s := f.session(); s.AgentSessionID != "new-fork" {
		t.Fatalf("SessionStart(startup) from Z rebound to %q", s.AgentSessionID)
	}
}

func TestHookAgentMismatch(t *testing.T) {
	f := newCodexFixture(t, domain.OriginLaunched, domain.StateWorking)
	r := f.run(f.env("demo"), payload("Stop")) // Claude hook (no --agent) into a Codex session
	if r.Orphan != OrphanAgentMismatch || r.Applied || f.session().State != domain.StateWorking {
		t.Fatalf("claude hook into codex session: %+v", r.Outcome)
	}
	g := newFixture(t, domain.OriginLaunched, domain.StateWorking) // claude session
	r = g.run(g.env("demo"), payload("Stop"), codexHook(embeddedCodex, nil))
	if r.Orphan != OrphanAgentMismatch || r.Applied || g.session().State != domain.StateWorking {
		t.Fatalf("codex hook into claude session: %+v", r.Outcome)
	}
}

func TestUnknownAgentRecordsNothing(t *testing.T) {
	f := newFixture(t, domain.OriginLaunched, domain.StateWorking)
	r := f.run(f.env("demo"), payload("Stop"), func(c *Config) { c.Agent = "gemini" })
	if r.Applied || r.Err == nil || f.session().State != domain.StateWorking {
		t.Fatalf("outcome = %+v", r.Outcome)
	}
	if len(f.events(domain.EventHookReceived)) != 0 {
		t.Fatal("unknown agent hook was recorded")
	}
	if !strings.Contains(f.errorLog(), "gemini") {
		t.Errorf("unknown agent not logged: %q", f.errorLog())
	}
}

// The Claude goal hold never applies to Codex (its SessionStart is record
// only anyway); a Claude session keeps it.
func TestGoalHoldIsClaudeOnly(t *testing.T) {
	f := newGoalFixture(t, domain.OriginLaunched, domain.StateStarting, "do it")
	if _, err := f.st.DB().ExecContext(context.Background(), `UPDATE sessions SET agent_type = 'codex' WHERE id = ?`, f.sess.ID); err != nil {
		t.Fatal(err)
	}
	f.run(f.env("demo"), payload("SessionStart", "source", "startup"), codexHook(embeddedCodex, nil))
	if s := f.session(); s.State != domain.StateStarting || s.Confidence != domain.ConfidenceNone {
		t.Fatalf("codex goal session after SessionStart = %s/%s, want starting/none (record only)", s.State, s.Confidence)
	}
}
