package tmux

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recordRunner struct {
	reqs []Request
	err  error
}

func (r *recordRunner) Run(_ context.Context, req Request) (Result, error) {
	r.reqs = append(r.reqs, req)
	return Result{}, r.err
}

func TestSwitchClientCommand(t *testing.T) {
	if got := SwitchClientCommand("%3"); !reflect.DeepEqual(got, []string{"switch-client", "-t", "%3"}) {
		t.Fatalf("SwitchClientCommand = %q", got)
	}
	rec := &recordRunner{}
	c := NewClient(rec, Socket{Path: "/tmp/sock"})
	if err := c.SwitchClient(context.Background(), "%3"); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) != 1 || rec.reqs[0].Socket.Path != "/tmp/sock" ||
		!reflect.DeepEqual(rec.reqs[0].Commands, [][]string{{"switch-client", "-t", "%3"}}) {
		t.Fatalf("requests = %+v", rec.reqs)
	}
	if err := c.SwitchClient(context.Background(), ""); err == nil {
		t.Fatal("empty target accepted")
	}
	rec.err = errors.New("boom")
	if err := c.SwitchClient(context.Background(), "%3"); err == nil {
		t.Fatal("runner error swallowed")
	}
}

func TestAttachCmdArgvAndEnv(t *testing.T) {
	cmd, err := AttachCmd("/usr/bin/tmux", []string{"PATH=/bin", "TERM=xterm"}, Socket{Path: "/tmp/sock"}, "%3")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/tmux", "-S", "/tmp/sock", "attach-session", "-t", "%3"}
	if cmd.Path != "/usr/bin/tmux" || !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("cmd = %s %q, want %q", cmd.Path, cmd.Args, want)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"PATH=/bin", "TERM=xterm"}) {
		t.Fatalf("env = %q", cmd.Env)
	}
}

func TestInteractiveCmdValidation(t *testing.T) {
	cases := []struct {
		name  string
		bin   string
		sock  Socket
		flags []string
		cmd   []string
	}{
		{"no bin", "", Socket{Path: "/s"}, nil, []string{"attach-session"}},
		{"relative bin", "tmux", Socket{Path: "/s"}, nil, []string{"attach-session"}},
		{"no socket", "/usr/bin/tmux", Socket{}, nil, []string{"attach-session"}},
		{"no command", "/usr/bin/tmux", Socket{Path: "/s"}, nil, nil},
		{"flag without dash", "/usr/bin/tmux", Socket{Path: "/s"}, []string{"C"}, []string{"attach-session"}},
		{"socket flag smuggled", "/usr/bin/tmux", Socket{Path: "/s"}, []string{"-L"}, []string{"attach-session"}},
	}
	for _, c := range cases {
		if _, err := InteractiveCmd(c.bin, nil, c.sock, c.flags, c.cmd); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	cmd, err := InteractiveCmd("/usr/bin/tmux", nil, Socket{Name: "x"}, []string{"-C"}, []string{"attach-session", "-t", "fix it;"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/tmux", "-C", "-L", "x", "attach-session", "-t", `fix it\;`}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("args = %q, want %q", cmd.Args, want)
	}
}
