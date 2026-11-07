package claude_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// paneHandle starts a detached session running argv on srv (remain-on-exit
// on, like a launch) and returns the handle a launch would have produced.
func paneHandle(t *testing.T, srv *testenv.TmuxServer, name string, argv ...string) adapter.Handle {
	t.Helper()
	c, err := srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{Name: name, Dir: "/", Argv: argv})
	if err != nil {
		t.Fatal(err)
	}
	return adapter.Handle{
		Identity:    domain.Identity{Socket: srv.Socket, ServerPID: c.ServerPID, SessionID: c.SessionID, PaneID: c.PaneID, PanePID: c.PanePID},
		SessionName: name,
	}
}

type brokenRunner struct{}

func (brokenRunner) Run(context.Context, tmux.Request) (tmux.Result, error) {
	return tmux.Result{}, errors.New("exec: tmux: permission denied")
}

func observe(t *testing.T, a *claude.Adapter, h adapter.Handle) domain.Liveness {
	t.Helper()
	obs, err := a.Observe(context.Background(), h)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	return obs.Liveness
}

func TestObserveClassifiesEveryVerdict(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	a := claude.New(claude.Config{Runner: srv.Runner()})

	alive := paneHandle(t, srv, "alive", "/bin/sleep", "600")
	if l := observe(t, a, alive); !l.IsAlive() {
		t.Fatalf("alive = %v", l)
	}
	obs, _ := a.Observe(context.Background(), alive)
	if !obs.Pane.Found || obs.Pane.PanePID != alive.Identity.PanePID || obs.Pane.ServerPID != alive.Identity.ServerPID {
		t.Fatalf("observation = %+v", obs.Pane)
	}

	dead := paneHandle(t, srv, "dead", "/bin/sh", "-c", "exit 3")
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		l := observe(t, a, dead)
		return l.Status == domain.LivenessDead, l.String()
	})
	if l := observe(t, a, dead); l.ExitStatus != 3 {
		t.Fatalf("dead = %+v", l)
	}

	missing := alive
	missing.Identity.PaneID = "%999"
	if l := observe(t, a, missing); l.Reason != domain.ReasonPaneMissing {
		t.Fatalf("missing = %v", l)
	}
	reused := alive
	reused.Identity.PanePID++
	if l := observe(t, a, reused); l.Reason != domain.ReasonIdentityMismatch {
		t.Fatalf("pane pid mismatch = %v", l)
	}
	if l := observe(t, a, adapter.Handle{}); l.Reason != domain.ReasonNoIdentity {
		t.Fatalf("zero identity = %v", l)
	}

	broken := claude.New(claude.Config{Runner: brokenRunner{}})
	if l := observe(t, broken, alive); l.Status != domain.LivenessUnverified {
		t.Fatalf("probe failure = %v, want unverified (never disconnected)", l)
	}

	srv.Kill(t)
	if l := observe(t, a, alive); l.Reason != domain.ReasonServerGone {
		t.Fatalf("server killed = %v", l)
	}
}
