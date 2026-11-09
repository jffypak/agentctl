package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/tmux"
)

// Helper-process protocol (no TestMain, so no clash with other cards).
const (
	helperDB      = "AGENTCTL_TUI_HELPER_DB"
	helperSession = "AGENTCTL_TUI_HELPER_SESSION"
	helperState   = "AGENTCTL_TUI_HELPER_STATE"
)

// TestHelperTransition is not a test: TestLiveStateChange runs the test
// binary again with it selected, so the state change comes from another
// process exactly as a hook would write it.
func TestHelperTransition(t *testing.T) {
	db := os.Getenv(helperDB)
	if db == "" {
		t.Skip("helper process for TestLiveStateChangeFromAnotherProcess")
	}
	id, err := strconv.ParseInt(os.Getenv(helperSession), 10, 64)
	if err == nil {
		var st *store.Store
		if st, err = store.Open(context.Background(), db, store.Options{}); err == nil {
			_, err = st.Sessions().Transition(context.Background(), id, domain.AgentState(os.Getenv(helperState)),
				domain.ConfidenceHook, "permission prompt", domain.SourceHook)
			fmt.Println(time.Now().UnixNano()) // committed: the write time the test measures from
			_ = st.Close()
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// probe wraps the model inside a running program and publishes the rendered
// screen after every Update, so a teatest wait can look at the screen itself
// instead of the renderer's cell diffs.
type probe struct {
	Model
	latest *atomic.Value
	watch  func(screen string) bool // optional: when it first holds, hit records the time
	hit    *atomic.Int64            // unix nanos of the first render satisfying watch
}

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := p.Model.Update(msg)
	p.Model = next.(Model)
	scr := plain(p.Model.View().Content)
	p.latest.Store(scr)
	if p.watch != nil && p.hit.Load() == 0 && p.watch(scr) {
		p.hit.Store(time.Now().UnixNano())
	}
	return p, cmd
}

// emptyReader satisfies teatest.WaitFor; the condition reads the probe.
type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

// errRunner makes every tmux probe fail: reconcile stays unverified and
// changes nothing while the live test runs.
type errRunner struct{}

func (errRunner) Run(context.Context, tmux.Request) (tmux.Result, error) {
	return tmux.Result{}, errors.New("no tmux in this test")
}

func TestLiveStateChangeFromAnotherProcess(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	ctl := openController(t, f.env, errRunner{}) // default tunables: Poll 300ms
	row := func(scr, name string) string {
		for _, l := range strings.Split(scr, "\n") {
			if fs := strings.Fields(strings.TrimPrefix(strings.TrimSpace(l), ">")); len(fs) > 0 && fs[0] == name {
				return l
			}
		}
		return ""
	}
	latest, hit := &atomic.Value{}, &atomic.Int64{}
	latest.Store("")
	pr := probe{Model: New(context.Background(), Options{Controller: ctl, Cwd: f.repo.Root}), latest: latest, hit: hit,
		watch: func(scr string) bool { return strings.Contains(row(scr, "payments"), "awaiting input") }}
	tm := teatest.NewTestModel(t, pr, teatest.WithInitialTermSize(120, 40))
	teatest.WaitFor(t, emptyReader{}, func([]byte) bool { return strings.Contains(row(latest.Load().(string), "payments"), "working") },
		teatest.WithDuration(5*time.Second), teatest.WithCheckInterval(10*time.Millisecond))

	sess, err := f.st.Sessions().LatestForTask(context.Background(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := paths.Resolve(f.env.Getenv)
	cmd := exec.Command(exe, "-test.run=^TestHelperTransition$")
	cmd.Env = append(f.env.Strings(), helperDB+"="+p.DB, helperSession+"="+strconv.FormatInt(sess.ID, 10), helperState+"="+string(domain.StateAwaitingInput))
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helper process: %v", err)
	}
	writtenNS, err := strconv.ParseInt(strings.TrimSpace(strings.Split(string(out), "\n")[0]), 10, 64)
	if err != nil {
		t.Fatalf("helper output %q: %v", out, err)
	}
	teatest.WaitFor(t, emptyReader{}, func([]byte) bool { return hit.Load() != 0 },
		teatest.WithDuration(2*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
	lag := time.Duration(hit.Load() - writtenNS)
	t.Logf("state change rendered %v after the other process committed it", lag.Round(time.Millisecond))
	if lag > time.Second {
		t.Fatalf("state change took %v to appear, want <= 1s", lag)
	}
	if scr := latest.Load().(string); !strings.Contains(scr, "agents / repo · 1 running · 6 need you") {
		t.Fatalf("header did not follow:\n%s", scr)
	}

	tm.Send(keyMsg("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(5*time.Second))
}

// TestRunCancelledContextIsACleanExit: SIGHUP cancels the context cmd_tui
// passes in; Run must return nil and still record ui.opened / ui.closed.
func TestRunCancelledContextIsACleanExit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	ctx, cancel := context.WithCancel(context.Background())
	in, inW := io.Pipe()
	defer inW.Close()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Controller: f.ctl, Cwd: f.repo.Root},
			tea.WithInput(in), tea.WithOutput(&out), tea.WithoutSignals(), tea.WithWindowSize(80, 24))
	}()
	waitEvent := func(typ string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			evs, err := f.ctl.EventsSince(context.Background(), 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range evs {
				if e.Type == typ {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no %s event", typ)
	}
	waitEvent(domain.EventUIOpened)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
	waitEvent(domain.EventUIClosed)
	s, err := f.ctl.Stats(context.Background())
	if err != nil || s.Runs != 1 || s.RunsWithoutAction != 1 {
		t.Fatalf("stats = %+v, %v", s, err)
	}
}

// TestRandomKeysNeverPanic drives 500 seeded random keys, pastes and resizes
// (down to 0x0) through Update and View, executing the commands that answer
// quickly (attach verdicts, flashes, reloads).
func TestRandomKeysNeverPanic(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	f.rec.panes = []tmux.Pane{{SessionID: "$1", PaneID: "%1", PanePID: 4000, SessionName: "agentctl-auth-tests"}}
	keys := []string{"j", "k", "up", "down", "enter", "esc", "q", "/", "p", "d", "l", "n", "a", "x", "c", "f", "y",
		"backspace", "space", "ctrl+u", "z", "1", "é", "界", "closed"}
	for _, seed := range []uint64{1, 42} {
		rng := rand.New(rand.NewPCG(seed, 7))
		m := f.model(t, 80, 24, Options{})
		for i := 0; i < 500; i++ {
			var msg tea.Msg
			switch r := rng.IntN(25); {
			case r == 0:
				msg = tea.WindowSizeMsg{Width: rng.IntN(130), Height: rng.IntN(45)}
			case r == 1:
				msg = tea.PasteMsg{Content: "x\x1b[31my\nz\x00"}
			case r == 2:
				msg = flashMsg{text: strings.Repeat("long flash ", 20), isErr: true}
			default:
				k := keys[rng.IntN(len(keys))]
				if k == "closed" {
					m = press(t, m, "/", "c", "l", "o", "s", "e", "d")
					continue
				}
				msg = keyMsg(k)
			}
			next, cmd := m.Update(msg)
			m = run(t, next.(Model), cmd)
			out := m.View().Content
			if strings.ContainsRune(plain(out), '\x1b') {
				t.Fatalf("seed %d step %d: raw escape reached the screen", seed, i)
			}
		}
	}
}

// TestNoAnimation: nothing on screen changes while nothing happens, and no
// animated bubbles (spinner, progress, stopwatch, timer) are imported.
func TestNoAnimation(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	m := f.model(t, 80, 24, Options{})
	before := m.View().Content
	for i := 0; i < 3; i++ {
		m = run(t, m, pollCmd(m.sh, m.lastID, true))
		m = update(t, m, reconciledMsg{})
	}
	if m.View().Content != before {
		t.Fatal("the screen changed without any state change")
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range af.Imports {
			for _, banned := range []string{"/spinner", "/progress", "/stopwatch", "/timer", "harmonica"} {
				if strings.Contains(imp.Path.Value, banned) {
					t.Errorf("%s imports %s", file, imp.Path.Value)
				}
			}
		}
	}
}
