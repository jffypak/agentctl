package tmux

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCaptureCommands(t *testing.T) {
	target := CaptureTarget{PaneID: "%7", ServerPID: 111, PanePID: 222}
	got, err := CaptureCommands(target, 500)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{
		"if-shell", "-F", "-t", "%7",
		"#{&&:#{==:#{pid},111},#{==:#{pane_pid},222}}",
		"display-message -p agentctl-capture-ok ; capture-pane -p -J -t %7 -S -500 -E -",
		"display-message -p -t %7 '#{?pane_id,agentctl-capture-mismatch,agentctl-capture-missing}'",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CaptureCommands =\n%q\nwant\n%q", got, want)
	}
	all, _ := CaptureCommands(target, CaptureAll)
	if all[0][5] != "display-message -p agentctl-capture-ok ; capture-pane -p -J -t %7 -S - -E -" {
		t.Fatalf("CaptureAll = %q", all[0][5])
	}
	screen, _ := CaptureCommands(target, 0)
	if screen[0][5] != "display-message -p agentctl-capture-ok ; capture-pane -p -J -t %7 -S 0 -E -" {
		t.Fatalf("visible only = %q", screen[0][5])
	}
	for _, bad := range []CaptureTarget{
		{PaneID: "", ServerPID: 1, PanePID: 1},
		{PaneID: "7", ServerPID: 1, PanePID: 1},
		{PaneID: "%7 ; display-message x", ServerPID: 1, PanePID: 1},
		{PaneID: "%7x", ServerPID: 1, PanePID: 1},
		{PaneID: "%", ServerPID: 1, PanePID: 1},
		{PaneID: "%7", ServerPID: 0, PanePID: 1},
		{PaneID: "%7", ServerPID: 1, PanePID: 0},
	} {
		if _, err := CaptureCommands(bad, 10); err == nil {
			t.Errorf("CaptureCommands(%+v) accepted", bad)
		}
	}
	if _, err := CaptureCommands(target, -2); err == nil {
		t.Error("negative history other than CaptureAll accepted")
	}
}

type cannedRunner struct {
	res Result
	err error
	req Request
}

func (r *cannedRunner) Run(_ context.Context, req Request) (Result, error) {
	r.req = req
	return r.res, r.err
}

func TestCapturePaneParsesMarker(t *testing.T) {
	target := CaptureTarget{PaneID: "%1", ServerPID: 5, PanePID: 6}
	ctx := context.Background()
	ok := &cannedRunner{res: Result{Stdout: []byte("agentctl-capture-ok\nline 1\n\nline 3\n")}}
	text, err := NewClient(ok, Socket{Path: "/s"}).CapturePane(ctx, target, 10)
	if err != nil || text != "line 1\n\nline 3\n" {
		t.Fatalf("CapturePane = %q %v", text, err)
	}
	if ok.req.Socket.Path != "/s" || len(ok.req.Commands) != 1 || ok.req.Commands[0][0] != "if-shell" {
		t.Fatalf("request = %+v", ok.req)
	}
	empty := &cannedRunner{res: Result{Stdout: []byte("agentctl-capture-ok\n")}}
	if text, err := NewClient(empty, Socket{Path: "/s"}).CapturePane(ctx, target, 10); err != nil || text != "" {
		t.Fatalf("empty pane = %q %v", text, err)
	}
	mismatch := &cannedRunner{res: Result{Stdout: []byte("agentctl-capture-mismatch\n")}}
	if text, err := NewClient(mismatch, Socket{Path: "/s"}).CapturePane(ctx, target, 10); !errors.Is(err, ErrPaneMismatch) || text != "" {
		t.Fatalf("mismatch = %q %v", text, err)
	}
	missing := &cannedRunner{res: Result{Stdout: []byte("agentctl-capture-missing\n")}}
	if text, err := NewClient(missing, Socket{Path: "/s"}).CapturePane(ctx, target, 10); !errors.Is(err, ErrPaneNotFound) || text != "" {
		t.Fatalf("missing = %q %v", text, err)
	}
	garbage := &cannedRunner{res: Result{Stdout: []byte("someone else's output\n")}}
	if text, err := NewClient(garbage, Socket{Path: "/s"}).CapturePane(ctx, target, 10); err == nil || text != "" {
		t.Fatalf("unmarked output = %q %v", text, err)
	}
	gone := &cannedRunner{err: newError([]string{"if-shell"}, "can't find pane: %1", errors.New("exit status 1"))}
	if _, err := NewClient(gone, Socket{Path: "/s"}).CapturePane(ctx, target, 10); !errors.Is(err, ErrPaneNotFound) {
		t.Fatalf("missing pane = %v, want ErrPaneNotFound", err)
	}
	if _, err := NewClient(&cannedRunner{}, Socket{Path: "/s"}).CapturePane(ctx, CaptureTarget{PaneID: "bad"}, 10); err == nil {
		t.Fatal("invalid target accepted")
	}
}
