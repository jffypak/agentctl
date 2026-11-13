package claude_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/testenv"
)

func TestStopKillsOnlyTheVerifiedSession(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	a := claude.New(claude.Config{Runner: srv.Runner()})
	ctx := context.Background()
	target := paneHandle(t, srv, "agentctl-target", "/bin/sleep", "600")
	bystander := paneHandle(t, srv, "bystander", "/bin/sleep", "600")

	if err := a.Stop(ctx, target); err != nil {
		t.Fatal(err)
	}
	snap := srv.Snapshot(t)
	if _, ok := snap.Find(target.Identity.PaneID); ok {
		t.Fatal("target still running after Stop")
	}
	if _, ok := snap.Find(bystander.Identity.PaneID); !ok {
		t.Fatal("Stop killed a bystander")
	}
	// stopping again: the pane is gone, identity cannot be verified -> refused
	if err := a.Stop(ctx, target); !errors.Is(err, claude.ErrStopRefused) {
		t.Fatalf("second Stop = %v, want ErrStopRefused", err)
	}

	// a dead (remain-on-exit) pane is still ours: Stop cleans its session up
	dead := paneHandle(t, srv, "agentctl-dead", "/bin/sh", "-c", "exit 0")
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		p, _ := srv.Snapshot(t).Find(dead.Identity.PaneID)
		return p.Dead, ""
	})
	if err := a.Stop(ctx, dead); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.Snapshot(t).Find(dead.Identity.PaneID); ok {
		t.Fatal("dead pane's session not removed")
	}
}

func TestStopRefusesAReusedPaneAfterServerRestart(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	a := claude.New(claude.Config{Runner: srv.Runner()})
	stale := paneHandle(t, srv, "jpark-demo", "/bin/sleep", "600")
	srv.Restart(t)
	intruder := paneHandle(t, srv, "jpark-demo", "/bin/sleep", "600")
	if intruder.Identity.PaneID != stale.Identity.PaneID {
		t.Fatalf("expected %%N reuse: %s vs %s", intruder.Identity.PaneID, stale.Identity.PaneID)
	}
	err := a.Stop(context.Background(), stale)
	if !errors.Is(err, claude.ErrStopRefused) {
		t.Fatalf("Stop(stale) = %v, want ErrStopRefused", err)
	}
	if p, ok := srv.Snapshot(t).Find(intruder.Identity.PaneID); !ok || p.PanePID != intruder.Identity.PanePID || p.Dead {
		t.Fatal("the pane that reused the stale ids was harmed")
	}
}

func TestStopRefusesWhenTheProbeFails(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	srv := testenv.NewTmuxServer(t, env)
	h := paneHandle(t, srv, "jpark-demo", "/bin/sleep", "600")
	broken := claude.New(claude.Config{Runner: brokenRunner{}})
	if err := broken.Stop(context.Background(), h); !errors.Is(err, claude.ErrStopRefused) {
		t.Fatalf("Stop with a failing probe = %v, want ErrStopRefused", err)
	}
	if p, ok := srv.Snapshot(t).Find(h.Identity.PaneID); !ok || p.Dead {
		t.Fatal("pane harmed")
	}
	if l := observe(t, claude.New(claude.Config{Runner: srv.Runner()}), h); l.Status != domain.LivenessAlive {
		t.Fatalf("after refused stop = %v", l)
	}
}
