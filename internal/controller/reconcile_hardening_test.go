package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/tmux"
)

// dyingRunner answers every tmux call the way a server that is shutting down
// does: it still owns the socket but drops each new client with stderr. The
// real dying state is reproduced against a live server in
// internal/tmux/restart_hardening_test.go; here it is pinned to the message.
type dyingRunner struct{ stderr string }

func (r dyingRunner) Run(_ context.Context, req tmux.Request) (tmux.Result, error) {
	names := make([]string, 0, len(req.Commands))
	for _, c := range req.Commands {
		names = append(names, c[0])
	}
	return tmux.Result{}, &tmux.Error{Commands: names, Stderr: r.stderr, Err: errors.New("exit status 1")}
}

// TestReconcileAgainstADyingServerIsServerGone: a reconcile tick that reaches
// the user's tmux server while it is exiting must record server_gone (it is
// going away), not fail the tick with a probe error and leave the sessions
// unverified.
func TestReconcileAgainstADyingServerIsServerGone(t *testing.T) {
	t.Parallel()
	for _, stderr := range []string{"server exited unexpectedly", "lost server"} {
		t.Run(stderr, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, sleepAgent)
			piNewTask(t, f.ctl, f.repo, "demo")
			piNewTask(t, f.ctl, f.repo, "other")

			dying, err := Open(context.Background(), Config{
				Environ: f.env.Strings(), AgentctlPath: "/opt/agentctl/bin/agentctl", TmuxRunner: dyingRunner{stderr},
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = dying.Close() })

			piReconcile(t, dying)
			assertDisconnected(t, dying, "demo", domain.ReasonServerGone)
			assertDisconnected(t, dying, "other", domain.ReasonServerGone)
		})
	}
}
