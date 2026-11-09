package tmux_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

func TestBufferBuilders(t *testing.T) {
	target := tmux.WriteTarget{PaneID: "%3", ServerPID: 10, PanePID: 20}
	cmds, err := tmux.LoadPasteCommands(target, tmux.BufferName(7))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cmds[0], " "); got != "load-buffer -b agentctl-msg-7 -" {
		t.Fatalf("load = %q", got)
	}
	ifs := cmds[1]
	if ifs[0] != "if-shell" || ifs[3] != "%3" || !strings.Contains(ifs[4], "#{==:#{pid},10}") ||
		!strings.Contains(ifs[4], "#{==:#{pane_pid},20}") || !strings.Contains(ifs[4], "#{==:#{pane_id},%3}") ||
		!strings.Contains(ifs[4], "pane_in_mode") || !strings.Contains(ifs[5], "paste-buffer -p -d -b agentctl-msg-7 -t %3") ||
		!strings.Contains(ifs[6], "delete-buffer -b agentctl-msg-7") {
		t.Fatalf("paste = %q", ifs)
	}
	enter, err := tmux.SendEnterCommand(target)
	if err != nil || !strings.Contains(enter[5], "send-keys -t %3 Enter") {
		t.Fatalf("enter = %q %v", enter, err)
	}
	for _, bad := range []tmux.WriteTarget{{PaneID: "3", ServerPID: 1, PanePID: 1}, {PaneID: "%3;x", ServerPID: 1, PanePID: 1}, {PaneID: "%3"}} {
		if _, err := tmux.LoadPasteCommands(bad, tmux.BufferName(1)); err == nil {
			t.Errorf("target %+v accepted", bad)
		}
	}
	if _, err := tmux.LoadPasteCommands(target, "agentctl-msg-1 ; kill"); err == nil {
		t.Error("bad buffer name accepted")
	}
}

// pasteRig runs fakeagent (no hooks) in an isolated tmux pane; its input
// log records every paste and submit with the text received.
type pasteRig struct {
	srv    *testenv.TmuxServer
	target tmux.WriteTarget
	log    string
}

func newPasteRig(t *testing.T) *pasteRig {
	t.Helper()
	fake := testenv.BuildFakeAgent(t)
	srv := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settings, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "input.jsonl")
	created, err := srv.Client().NewSession(context.Background(), tmux.NewSessionSpec{
		Name: "paste", Dir: dir, Argv: []string{"/usr/bin/env", "FAKEAGENT_LOG=" + log, fake, "--settings", settings},
	})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, 0, "fakeagent prompt", func() (bool, string) {
		out, _ := srv.TryRun("capture-pane", "-p", "-t", created.PaneID)
		return strings.HasSuffix(strings.TrimSpace(out), ">"), out
	})
	return &pasteRig{srv: srv, target: tmux.WriteTarget{PaneID: created.PaneID, ServerPID: created.ServerPID, PanePID: created.PanePID}, log: log}
}

type inputEntry struct {
	Text  string `json:"text"`
	Kind  string `json:"kind"`
	State string `json:"state_at_receipt"`
}

func (r *pasteRig) inputs(t *testing.T) []inputEntry {
	t.Helper()
	f, err := os.Open(r.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []inputEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var e inputEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestLoadPasteEnterIntegration(t *testing.T) {
	t.Parallel()
	r := newPasteRig(t)
	c, ctx := r.srv.Client(), context.Background()
	body := "first line\nsecond line; with a semicolon\n\tindented #{pane_id} $HOME"
	if err := c.LoadAndPaste(ctx, r.target, tmux.BufferName(1), body); err != nil {
		t.Fatal(err)
	}
	if err := c.SendEnter(ctx, r.target); err != nil {
		t.Fatal(err)
	}
	var got []inputEntry
	testenv.WaitFor(t, 0, "paste + submit logged", func() (bool, string) {
		got = r.inputs(t)
		return len(got) >= 2, strings.Repeat("x", len(got))
	})
	if got[0].Kind != "paste" || got[0].Text != body || got[1].Kind != "submit" || got[1].Text != body {
		t.Fatalf("inputs = %+v, want the multi-line body pasted once and submitted", got)
	}
	bufs, err := c.ListBuffers(ctx)
	if err != nil || len(bufs) != 0 {
		t.Fatalf("buffers left after delivery: %v %v", bufs, err)
	}
}

func TestPasteRefusedForAnotherIdentityOrCopyMode(t *testing.T) {
	t.Parallel()
	r := newPasteRig(t)
	c, ctx := r.srv.Client(), context.Background()
	for name, bad := range map[string]tmux.WriteTarget{
		"pane pid":   {PaneID: r.target.PaneID, ServerPID: r.target.ServerPID, PanePID: r.target.PanePID + 100000},
		"server pid": {PaneID: r.target.PaneID, ServerPID: r.target.ServerPID + 1, PanePID: r.target.PanePID},
		"missing":    {PaneID: "%999", ServerPID: r.target.ServerPID, PanePID: r.target.PanePID},
	} {
		if err := c.LoadAndPaste(ctx, bad, tmux.BufferName(2), "secret"); !errors.Is(err, tmux.ErrPasteRefused) {
			t.Errorf("%s: paste = %v, want ErrPasteRefused", name, err)
		}
		if err := c.SendEnter(ctx, bad); !errors.Is(err, tmux.ErrPasteRefused) {
			t.Errorf("%s: enter = %v, want ErrPasteRefused", name, err)
		}
	}
	r.srv.Run(t, "copy-mode", "-t", r.target.PaneID)
	err := c.LoadAndPaste(ctx, r.target, tmux.BufferName(3), "secret")
	if !errors.Is(err, tmux.ErrPasteRefused) || !strings.Contains(err.Error(), "copy mode") {
		t.Fatalf("copy mode paste = %v", err)
	}
	if bufs, _ := c.ListBuffers(ctx); len(bufs) != 0 {
		t.Fatalf("refused pastes left buffers: %v", bufs)
	}
	if got := r.inputs(t); len(got) != 0 {
		t.Fatalf("a refused write reached the agent: %+v", got)
	}
}
