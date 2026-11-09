package testenv

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/tmux"
)

// ControlClient is a tmux control-mode client (`tmux -C attach-session`) on
// an isolated server. It is a real attached client that needs no terminal, so
// tests can watch switch-client move it (list-clients -F '#{client_session}')
// exactly as it would move the user's terminal.
type ControlClient struct {
	srv   *TmuxServer
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{}
}

// controlClientWait bounds attach and detach.
const controlClientWait = 5 * time.Second

// AttachControlClient attaches a control-mode client to target (a session
// name or id) and waits until the server lists it. The client detaches in
// t.Cleanup (closing its stdin ends control mode).
func (s *TmuxServer) AttachControlClient(t testing.TB, target string) *ControlClient {
	t.Helper()
	bin := RequireTool(t, "tmux")
	cmd, err := tmux.InteractiveCmd(bin, s.Env.Strings(), tmux.Socket{Path: s.Socket}, []string{"-C"}, tmux.AttachSessionCommand(target))
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start control client: %v", err)
	}
	c := &ControlClient{srv: s, cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		_, _ = io.Copy(io.Discard, stdout) // control mode notifications; never let the pipe fill
		_ = cmd.Wait()
		close(c.done)
	}()
	t.Cleanup(c.Close)
	WaitFor(t, controlClientWait, "control client to attach", func() (bool, string) {
		out, err := s.TryRun("list-clients", "-F", "#{client_session}")
		return err == nil && out != "", out
	})
	return c
}

// Session returns the session the server reports for this client. The
// isolated server has only this client, so list-clients has one line.
func (c *ControlClient) Session(t testing.TB) string {
	t.Helper()
	out := c.srv.Run(t, "list-clients", "-F", "#{client_session}")
	if strings.Contains(out, "\n") {
		t.Fatalf("expected exactly one client, list-clients printed %q", out)
	}
	return out
}

// Close detaches the client and waits for it to exit (killing only this
// client process if it does not).
func (c *ControlClient) Close() {
	_ = c.stdin.Close()
	ctx, cancel := context.WithTimeout(context.Background(), controlClientWait)
	defer cancel()
	select {
	case <-c.done:
	case <-ctx.Done():
		_ = c.cmd.Process.Kill()
		<-c.done
	}
}
