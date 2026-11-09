package claude_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/tmux"
)

// scriptRunner answers each invocation with the next canned stdout/error and
// records the requests (with stdin read out).
type scriptRunner struct {
	mu      sync.Mutex
	answers []answer
	reqs    []tmux.Request
	stdin   []string
}

type answer struct {
	out string
	err error
}

func (r *scriptRunner) Run(_ context.Context, req tmux.Request) (tmux.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	in := ""
	if req.Stdin != nil {
		b, _ := io.ReadAll(req.Stdin)
		in = string(b)
	}
	r.stdin = append(r.stdin, in)
	if len(r.answers) == 0 {
		return tmux.Result{}, nil
	}
	a := r.answers[0]
	r.answers = r.answers[1:]
	return tmux.Result{Stdout: []byte(a.out)}, a.err
}

func deliverHandle() adapter.Handle {
	return adapter.Handle{Identity: domain.Identity{Socket: "/tmp/tmux-1/s", ServerPID: 10, SessionID: "$1", PaneID: "%4", PanePID: 20}}
}

func TestDeliverPastesThenEnters(t *testing.T) {
	r := &scriptRunner{answers: []answer{{out: "agentctl-paste-ok\n"}, {out: "agentctl-enter-ok\n"}}}
	sent := time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC)
	a := claude.New(claude.Config{Runner: r, Now: func() time.Time { return sent }})
	ctx := claude.WithPasteEnterDelay(context.Background(), time.Millisecond)
	rc, err := a.Deliver(ctx, deliverHandle(), adapter.Message{ID: 9, Body: "a\nb"})
	if err != nil || rc.Buffer != "agentctl-msg-9" || !rc.SentAt.Equal(sent) {
		t.Fatalf("receipt = %+v %v", rc, err)
	}
	if len(r.reqs) != 2 || r.stdin[0] != "a\nb" || r.reqs[0].Socket.Path != "/tmp/tmux-1/s" {
		t.Fatalf("requests = %+v stdin %q", r.reqs, r.stdin)
	}
	if !strings.Contains(strings.Join(r.reqs[0].Commands[1], " "), "paste-buffer -p -d -b agentctl-msg-9 -t %4") ||
		!strings.Contains(strings.Join(r.reqs[1].Commands[0], " "), "send-keys -t %4 Enter") {
		t.Fatalf("commands = %q / %q", r.reqs[0].Commands, r.reqs[1].Commands)
	}
}

func TestDeliverErrorContract(t *testing.T) {
	ctx := claude.WithPasteEnterDelay(context.Background(), 0)
	// refused by the in-server guard: nothing pasted -> ErrNotReady, no Enter
	r := &scriptRunner{answers: []answer{{out: "agentctl-paste-refused identity changed\n"}}}
	_, err := claude.New(claude.Config{Runner: r}).Deliver(ctx, deliverHandle(), adapter.Message{ID: 1, Body: "x"})
	if !errors.Is(err, adapter.ErrNotReady) || len(r.reqs) != 1 {
		t.Fatalf("refused paste = %v (%d calls)", err, len(r.reqs))
	}
	// tmux failed: outcome unknown -> not ErrNotReady, buffer deleted
	r = &scriptRunner{answers: []answer{{err: errors.New("boom")}}}
	_, err = claude.New(claude.Config{Runner: r}).Deliver(ctx, deliverHandle(), adapter.Message{ID: 2, Body: "x"})
	if err == nil || errors.Is(err, adapter.ErrNotReady) || len(r.reqs) != 2 || r.reqs[1].Commands[0][0] != "delete-buffer" {
		t.Fatalf("failed paste = %v, reqs %+v", err, r.reqs)
	}
	// pasted, Enter refused -> ErrNotSubmitted (never ErrNotReady)
	r = &scriptRunner{answers: []answer{{out: "agentctl-paste-ok\n"}, {out: "agentctl-enter-refused pane in copy mode\n"}}}
	_, err = claude.New(claude.Config{Runner: r}).Deliver(ctx, deliverHandle(), adapter.Message{ID: 3, Body: "x"})
	if !errors.Is(err, claude.ErrNotSubmitted) || errors.Is(err, adapter.ErrNotReady) {
		t.Fatalf("refused enter = %v", err)
	}
	// no identity
	_, err = claude.New(claude.Config{Runner: r}).Deliver(ctx, adapter.Handle{}, adapter.Message{ID: 4, Body: "x"})
	if !errors.Is(err, adapter.ErrNotReady) {
		t.Fatalf("no identity = %v", err)
	}
}
