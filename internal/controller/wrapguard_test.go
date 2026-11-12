package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// newWrappedRig is a pqRig whose session is a wrapped Codex session, with an
// injected process table.
func newWrappedRig(t *testing.T, procs func(panePID int) ([]proc, error)) *pqRig {
	t.Helper()
	r := newPQRig(t, pqTunables())
	if _, err := r.ctl.store.DB().Exec(`UPDATE sessions SET origin = 'wrapped', agent_type = 'codex' WHERE id = ?`, r.sess.ID); err != nil {
		t.Fatal(err)
	}
	pid := r.sess.Identity.PanePID
	r.ctl.procs = func(context.Context) ([]proc, error) { return procs(pid) }
	return r
}

// shellWithChild: the pane's shell (pid) in the foreground group fg with one
// child running args.
func shellWithChild(pid int, args string) []proc {
	return []proc{
		{PID: pid, PPID: 1, PGID: pid, TPGID: pid + 1, TTY: "ttys009", Args: "-zsh"},
		{PID: pid + 1, PPID: pid, PGID: pid + 1, TPGID: pid + 1, TTY: "ttys009", Args: args},
		{PID: pid + 2, PPID: pid + 1, PGID: pid + 1, TPGID: pid + 1, TTY: "ttys009", Args: "codex -c hooks.Stop=[...]"},
	}
}

// Review Focus 1: the wrapper was killed (SIGKILL) and could not record
// Codex's exit: the session still says ready/hook, but the pane is back at
// the user's shell. Nothing is pasted; the message stays queued.
func TestDrainWrappedRefusesWhenCodexNotForeground(t *testing.T) {
	t.Parallel()
	r := newWrappedRig(t, func(pid int) ([]proc, error) {
		return []proc{{PID: pid, PPID: 1, PGID: pid, TPGID: pid, TTY: "ttys009", Args: "-zsh"}}, nil
	})
	m := r.enqueue("rm -rf ~/oops")
	dr, err := r.ctl.Drain(context.Background(), r.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dr.Delivered || dr.Reason != domain.LabelNotForeground {
		t.Fatalf("drain = %+v, want refused with %q", dr, domain.LabelNotForeground)
	}
	if got := r.message(m.ID); got.State != domain.MsgQueued {
		t.Fatalf("message state = %s, want queued", got.State)
	}
	if p := r.pastes(); len(p) != 0 {
		t.Fatalf("pasted into the pane: %+v", p)
	}
	r.noBuffers()
}

// A process table that cannot be read is not proof: no paste.
func TestDrainWrappedRefusesWhenPSFails(t *testing.T) {
	t.Parallel()
	r := newWrappedRig(t, func(int) ([]proc, error) { return nil, errors.New("ps: permission denied") })
	m := r.enqueue("hello")
	dr, err := r.ctl.Drain(context.Background(), r.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dr.Delivered || r.message(m.ID).State != domain.MsgQueued || len(r.pastes()) != 0 {
		t.Fatalf("drain = %+v", dr)
	}
}

// Another program in the foreground (e.g. the agentctl TUI itself) is not
// the wrapper either.
func TestDrainWrappedRefusesOtherAgentctl(t *testing.T) {
	t.Parallel()
	r := newWrappedRig(t, func(pid int) ([]proc, error) { return shellWithChild(pid, "/opt/agentctl/bin/agentctl tui"), nil })
	m := r.enqueue("hello")
	if dr, _ := r.ctl.Drain(context.Background(), r.sess.ID); dr.Delivered || r.message(m.ID).State != domain.MsgQueued {
		t.Fatalf("drain = %+v", dr)
	}
}

func TestDrainWrappedDeliversWhenWrapperForeground(t *testing.T) {
	t.Parallel()
	r := newWrappedRig(t, func(pid int) ([]proc, error) {
		return shellWithChild(pid, "/opt/agentctl/bin/agentctl codex --name w"), nil
	})
	m := r.enqueue("hello")
	dr, err := r.ctl.Drain(context.Background(), r.sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !dr.Delivered {
		t.Fatalf("drain = %+v, want delivered", dr)
	}
	if p := r.waitPastes(1); p[0].Text != "hello" {
		t.Fatalf("pastes = %+v", p)
	}
	if got := r.message(m.ID); got.State != domain.MsgSent && got.State != domain.MsgAcknowledged {
		t.Fatalf("message state = %s", got.State)
	}
}

// Launched sessions never consult the process table.
func TestDrainLaunchedIgnoresForeground(t *testing.T) {
	t.Parallel()
	r := newPQRig(t, pqTunables())
	r.ctl.procs = func(context.Context) ([]proc, error) { return nil, errors.New("must not be called") }
	r.enqueue("hello")
	if dr, err := r.ctl.Drain(context.Background(), r.sess.ID); err != nil || !dr.Delivered {
		t.Fatalf("drain = %+v %v", dr, err)
	}
	_ = store.Message{}
}
