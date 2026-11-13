package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
	"github.com/jffypak/agentctl/internal/tmux"
)

// atSnapRunner answers snapshot probes with a canned server and records every
// request; any other command succeeds silently.
type atSnapRunner struct {
	mu        sync.Mutex
	serverPID int
	panes     []tmux.Pane
	reqs      []tmux.Request
}

func (r *atSnapRunner) Run(_ context.Context, req tmux.Request) (tmux.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	if len(req.Commands) == 2 && req.Commands[0][0] == "display-message" && req.Commands[1][0] == "list-panes" {
		var b strings.Builder
		fmt.Fprintf(&b, "%d\n", r.serverPID)
		for _, p := range r.panes {
			fmt.Fprintf(&b, "%d\t%s\t0\t%s\t%d\t0\t\t\t%s\tclaude\t/w\n", r.serverPID, p.SessionID, p.PaneID, p.PanePID, p.SessionName)
		}
		return tmux.Result{Stdout: []byte(b.String())}, nil
	}
	return tmux.Result{}, nil
}

func (r *atSnapRunner) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, req := range r.reqs {
		for _, c := range req.Commands {
			out = append(out, c[0])
		}
	}
	return out
}

func atOpenWithRunner(t *testing.T, env testenv.Env, r tmux.Runner) *Controller {
	t.Helper()
	c, err := Open(context.Background(), Config{Environ: env.Strings(), TmuxRunner: r, AgentctlPath: "/opt/agentctl/bin/agentctl"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// seedSession reserves task id with one session on socket /s, server 100,
// pane %7 (pid 700), then walks it through states.
func atSeedSession(t *testing.T, ctl *Controller, id string, identity domain.Identity, initial domain.AgentState, path ...domain.AgentState) {
	t.Helper()
	ctx := context.Background()
	repo, err := ctl.store.Repos().Upsert(ctx, "/r/main", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.store.Tasks().Reserve(ctx, store.Task{ID: id, RepoID: repo.ID, Name: id, WorktreePath: "/w/" + id, Branch: "jpark/" + id}); err != nil {
		t.Fatal(err)
	}
	s, err := ctl.store.Sessions().Create(ctx, store.NewSession{TaskID: id, AgentType: "claude", Origin: domain.OriginLaunched,
		Identity: identity, SessionName: "agentctl-" + id, State: initial, Confidence: domain.ConfidenceNone})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range path {
		detail := ""
		if st == domain.StateDisconnected {
			detail = "disconnected: pane_missing"
		}
		if _, err := ctl.store.Sessions().Transition(ctx, s.ID, st, domain.ConfidencePane, detail, domain.SourcePane); err != nil {
			t.Fatal(err)
		}
	}
}

var atIdentity = domain.Identity{Socket: "/s", ServerPID: 100, SessionID: "$1", PaneID: "%7", PanePID: 700}

func TestAttachRefusalsAreOneLineAndNeverSwitch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		seed      func(t *testing.T, ctl *Controller)
		task      string
		snapPID   int
		panes     []tmux.Pane
		kind      error
		want      string
		noTmux    bool // refused before any tmux call (stored state rules it out)
		probeOnly bool // only the read-only snapshot ran
	}{
		{name: "unknown task", seed: func(*testing.T, *Controller) {}, task: "nope", kind: ErrTaskNotFound, want: `task "nope" not found`, noTmux: true},
		{name: "no session", task: "pending", kind: ErrAttachRefused, want: "attach refused: pending has no agent session", noTmux: true,
			seed: func(t *testing.T, ctl *Controller) {
				repo, _ := ctl.store.Repos().Upsert(context.Background(), "/r/main", "main")
				if _, err := ctl.store.Tasks().Reserve(context.Background(), store.Task{ID: "pending", RepoID: repo.ID, Name: "pending", WorktreePath: "/w/p", Branch: "b"}); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "stored disconnected", task: "gone", kind: ErrAttachRefused, want: "attach refused: gone is disconnected (pane_missing)", noTmux: true,
			seed: func(t *testing.T, ctl *Controller) {
				atSeedSession(t, ctl, "gone", atIdentity, domain.StateStarting, domain.StateDisconnected)
			}},
		{name: "launch failed, no identity", task: "broken", kind: ErrAttachRefused, want: "attach refused: broken has no recorded tmux pane", noTmux: true,
			seed: func(t *testing.T, ctl *Controller) {
				atSeedSession(t, ctl, "broken", domain.Identity{}, domain.StateFailed)
			}},
		{name: "server restarted, %N reused", task: "reused", kind: ErrAttachRefused, probeOnly: true,
			want: "attach refused: reused's pane is gone or was reused (identity_mismatch)", snapPID: 999,
			panes: []tmux.Pane{{SessionID: "$1", PaneID: "%7", PanePID: 700, SessionName: "intruder"}},
			seed: func(t *testing.T, ctl *Controller) {
				atSeedSession(t, ctl, "reused", atIdentity, domain.StateStarting, domain.StateWorking)
			}},
		{name: "pane pid differs", task: "swapped", kind: ErrAttachRefused, probeOnly: true,
			want: "attach refused: swapped's pane is gone or was reused (identity_mismatch)", snapPID: 100,
			panes: []tmux.Pane{{SessionID: "$1", PaneID: "%7", PanePID: 701, SessionName: "intruder"}},
			seed: func(t *testing.T, ctl *Controller) {
				atSeedSession(t, ctl, "swapped", atIdentity, domain.StateStarting, domain.StateWorking)
			}},
		{name: "pane missing", task: "missing", kind: ErrAttachRefused, probeOnly: true,
			want: "attach refused: missing's pane is gone or was reused (pane_missing)", snapPID: 100,
			seed: func(t *testing.T, ctl *Controller) {
				atSeedSession(t, ctl, "missing", atIdentity, domain.StateStarting, domain.StateWorking)
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rec := &atSnapRunner{serverPID: c.snapPID, panes: c.panes}
			env := testenv.NewEnv(t).With("TMUX=/s,100,0")
			ctl := atOpenWithRunner(t, env, rec)
			c.seed(t, ctl)
			res, err := ctl.Attach(context.Background(), c.task)
			if !errors.Is(err, c.kind) || err.Error() != c.want || strings.Contains(err.Error(), "\n") {
				t.Fatalf("Attach = %+v, %v; want %v %q", res, err, c.kind, c.want)
			}
			cmds := rec.commands()
			switch {
			case c.noTmux && len(cmds) != 0:
				t.Fatalf("tmux calls = %v, want none", cmds)
			case c.probeOnly && !slices.Equal(cmds, []string{"display-message", "list-panes"}):
				t.Fatalf("tmux calls = %v, want only the snapshot probe", cmds)
			}
		})
	}
}

func TestAttachUnverifiedProbeIsRefused(t *testing.T) {
	t.Parallel()
	ctl := atOpenWithRunner(t, testenv.NewEnv(t), failingRunner{})
	atSeedSession(t, ctl, "flaky", atIdentity, domain.StateStarting, domain.StateWorking)
	_, err := ctl.Attach(context.Background(), "flaky")
	if !errors.Is(err, ErrAttachRefused) || !strings.HasPrefix(err.Error(), "attach refused: cannot verify flaky's pane (") {
		t.Fatalf("Attach = %v", err)
	}
}

func TestAttachSameServerSwitchesClientToThePane(t *testing.T) {
	t.Parallel()
	rec := &atSnapRunner{serverPID: 100, panes: []tmux.Pane{{SessionID: "$1", PaneID: "%7", PanePID: 700, SessionName: "agentctl-live"}}}
	ctl := atOpenWithRunner(t, testenv.NewEnv(t).With("TMUX=/s,100,0", "TMUX_PANE=%2"), rec)
	atSeedSession(t, ctl, "live", atIdentity, domain.StateStarting, domain.StateAwaitingInput)
	res, err := ctl.Attach(context.Background(), "live")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Switched || res.Cmd != nil || res.PaneID != "%7" || res.SessionName != "agentctl-live" || res.TaskID != "live" {
		t.Fatalf("result = %+v", res)
	}
	last := rec.reqs[len(rec.reqs)-1]
	if last.Socket.Path != "/s" || !slices.Equal(last.Commands[0], []string{"switch-client", "-t", "%7"}) {
		t.Fatalf("last request = %+v", last)
	}
}

func TestAttachOutsideTmuxBuildsAnInteractiveAttachWithoutTMUX(t *testing.T) {
	t.Parallel()
	rec := &atSnapRunner{serverPID: 100, panes: []tmux.Pane{{SessionID: "$1", PaneID: "%7", PanePID: 700, SessionName: "agentctl-live"}}}
	// A different server in $TMUX (nested use) takes the exec path too.
	ctl := atOpenWithRunner(t, testenv.NewEnv(t).With("TMUX=/other,5,0", "TMUX_PANE=%9"), rec)
	atSeedSession(t, ctl, "live", atIdentity, domain.StateStarting, domain.StateStopped) // dead but verified panes may be read
	res, err := ctl.Attach(context.Background(), "live")
	if err != nil {
		t.Fatal(err)
	}
	if res.Switched || res.Cmd == nil {
		t.Fatalf("result = %+v", res)
	}
	if got := res.Cmd.Args[1:]; !slices.Equal(got, []string{"-S", "/s", "attach-session", "-t", "%7"}) {
		t.Fatalf("args = %q", got)
	}
	for _, kv := range res.Cmd.Env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			t.Fatalf("attach env keeps %s", kv)
		}
	}
	if slices.ContainsFunc(rec.commands(), func(c string) bool { return c != "display-message" && c != "list-panes" }) {
		t.Fatalf("exec path ran tmux commands itself: %v", rec.commands())
	}

	noTmux := atOpenWithRunner(t, testenv.NewEnv(t).With("PATH="+testenv.BinDir(t, "git")), rec)
	atSeedSession(t, noTmux, "live", atIdentity, domain.StateStarting, domain.StateWorking)
	if _, err := noTmux.Attach(context.Background(), "live"); !errors.Is(err, ErrTmuxMissing) {
		t.Fatalf("attach without tmux = %v", err)
	}
}

// TestAttachIntegrationControlMode is the acceptance check: inside the agent's
// tmux server, Attach moves the user's client (a control-mode client here) to
// the agent session; list-clients -F '#{client_session}' shows it.
func TestAttachIntegrationControlMode(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	if _, err := f.ctl.NewTask(ctx, NewTaskRequest{Name: "demo", Goal: "g", Cwd: f.repo.Root}); err != nil {
		t.Fatal(err)
	}
	f.srv.Run(t, "new-session", "-d", "-s", "home", "--", "sleep", "600")
	client := f.srv.AttachControlClient(t, "=home")

	inside := openController(t, f.env.With("TMUX="+f.srv.Socket+","+strconv.Itoa(f.srv.PID(t))+",0"))
	res, err := inside.Attach(ctx, "demo")
	if err != nil || !res.Switched {
		t.Fatalf("Attach = %+v, %v", res, err)
	}
	if got := client.Session(t); got != "agentctl-demo" {
		t.Fatalf("list-clients client_session = %q, want agentctl-demo", got)
	}
	if active := f.srv.Run(t, "display-message", "-p", "-t", "=agentctl-demo:", "#{pane_id}"); active != res.PaneID {
		t.Fatalf("active pane = %s, want %s", active, res.PaneID)
	}

	// Kill the pane: the stored identity no longer verifies, the attach is
	// refused and the client stays where it is.
	f.srv.Run(t, "new-session", "-d", "-s", "elsewhere", "--", "sleep", "600")
	if err := f.srv.Client().SwitchClient(ctx, "=elsewhere:"); err != nil {
		t.Fatal(err)
	}
	testenv.WaitFor(t, 0, "client on elsewhere", func() (bool, string) { got := client.Session(t); return got == "elsewhere", got })
	f.srv.Run(t, "respawn-pane", "-k", "-t", res.PaneID, "sleep", "600") // same %N, new pane pid
	if _, err := inside.Attach(ctx, "demo"); !errors.Is(err, ErrAttachRefused) {
		t.Fatalf("attach to a respawned pane = %v, want refused", err)
	}
	if got := client.Session(t); got != "elsewhere" {
		t.Fatalf("refused attach moved the client to %q", got)
	}
}

// TestAttachIntegrationExecPath runs the exec-path command inside a pane of a
// second isolated server (a stand-in for the user's terminal) and checks the
// agent's server now has a client on the agent session.
func TestAttachIntegrationExecPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	if _, err := f.ctl.NewTask(ctx, NewTaskRequest{Name: "demo", Goal: "g", Cwd: f.repo.Root}); err != nil {
		t.Fatal(err)
	}
	res, err := f.ctl.Attach(ctx, "demo")
	if err != nil || res.Cmd == nil {
		t.Fatalf("Attach = %+v, %v", res, err)
	}
	term := testenv.NewTmuxServer(t, testenv.NewEnv(t))
	args := append([]string{"new-session", "-d", "-s", "term", "-x", "100", "-y", "30", "--", "env", "-u", "TMUX", "-u", "TMUX_PANE", res.Cmd.Path}, res.Cmd.Args[1:]...)
	term.Run(t, args...)
	testenv.WaitFor(t, 0, "attached client on the agent server", func() (bool, string) {
		out, _ := f.srv.TryRun("list-clients", "-F", "#{client_session}")
		return out == "agentctl-demo", out
	})
}
