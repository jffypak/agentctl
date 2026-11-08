package tmux_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

func startPane(t *testing.T, srv *testenv.TmuxServer, name, script string) tmux.CaptureTarget {
	t.Helper()
	created, err := srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: name, Dir: t.TempDir(), Argv: []string{"/bin/sh", "-c", script},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tmux.CaptureTarget{PaneID: created.PaneID, ServerPID: created.ServerPID, PanePID: created.PanePID}
}

func waitForCapture(t *testing.T, c tmux.Client, target tmux.CaptureTarget, history int, want string) string {
	t.Helper()
	var text string
	testenv.WaitFor(t, 0, "pane output "+want, func() (bool, string) {
		var err error
		text, err = c.CapturePane(context.Background(), target, history)
		return err == nil && strings.Contains(text, want), text
	})
	return text
}

func TestCapturePaneIntegration(t *testing.T) {
	t.Parallel()
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	c, ctx := srv.Client(), context.Background()
	target := startPane(t, srv, "cap", `printf 'hello\033]52;c;ZXZpbA==\007 world\n\033[31mred\033[0m\n'; `+
		`i=1; while [ $i -le 300 ]; do echo "line $i"; i=$((i+1)); done; echo DONE; exec sleep 600`)

	all := waitForCapture(t, c, target, tmux.CaptureAll, "DONE")
	for _, want := range []string{"hello world", "red", "line 1\n", "line 300"} {
		if !strings.Contains(all, want) {
			t.Errorf("full capture is missing %q", want)
		}
	}
	if strings.ContainsAny(all, "\x1b\x07") || strings.Contains(all, "52;c") {
		t.Errorf("capture contains escape sequences: %q", all)
	}
	screen, err := c.CapturePane(ctx, target, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(screen, "line 1\n") || !strings.Contains(screen, "DONE") {
		t.Errorf("visible-only capture should hold the last screen only: %q", screen)
	}

	for name, bad := range map[string]tmux.CaptureTarget{
		"pane pid": {PaneID: target.PaneID, ServerPID: target.ServerPID, PanePID: target.PanePID + 100000},
		"server":   {PaneID: target.PaneID, ServerPID: target.ServerPID + 100000, PanePID: target.PanePID},
	} {
		text, err := c.CapturePane(ctx, bad, tmux.CaptureAll)
		if !errors.Is(err, tmux.ErrPaneMismatch) || text != "" {
			t.Errorf("%s mismatch: %q %v, want ErrPaneMismatch and no text", name, text, err)
		}
	}
	missing := tmux.CaptureTarget{PaneID: "%999", ServerPID: target.ServerPID, PanePID: target.PanePID}
	if _, err := c.CapturePane(ctx, missing, 0); !errors.Is(err, tmux.ErrPaneNotFound) {
		t.Errorf("missing pane = %v, want ErrPaneNotFound", err)
	}
}

func TestCapturePaneDeadPaneKeepsFinalOutput(t *testing.T) {
	t.Parallel()
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	target := startPane(t, srv, "dead", `echo "final words before exit"; exit 3`)
	testenv.WaitFor(t, 0, "dead pane", func() (bool, string) {
		p, _ := srv.Snapshot(t).Find(target.PaneID)
		return p.Dead, ""
	})
	text, err := srv.Client().CapturePane(context.Background(), target, tmux.CaptureAll)
	if err != nil || !strings.Contains(text, "final words before exit") {
		t.Fatalf("dead pane capture = %q %v", text, err)
	}
}

func TestCapturePaneReusedPaneIDIsNeverCaptured(t *testing.T) {
	t.Parallel()
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	c := srv.Client()
	old := startPane(t, srv, "old", `echo "old agent"; exec sleep 600`)
	waitForCapture(t, c, old, tmux.CaptureAll, "old agent")

	srv.Restart(t)
	reused := startPane(t, srv, "new", `echo "SECRET of another agent"; exec sleep 600`)
	if reused.PaneID != old.PaneID {
		t.Fatalf("restart did not reuse %s (got %s)", old.PaneID, reused.PaneID)
	}
	waitForCapture(t, c, reused, tmux.CaptureAll, "SECRET")
	text, err := c.CapturePane(context.Background(), old, tmux.CaptureAll)
	if !errors.Is(err, tmux.ErrPaneMismatch) || strings.Contains(text, "SECRET") {
		t.Fatalf("stale identity captured the reused pane: %q %v", text, err)
	}
}
