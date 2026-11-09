// Package hookingest is `agentctl hook`: Claude Code spawns it once per hook
// event with the payload JSON on stdin and the agent pane's environment
// (AGENTCTL_TASK_ID, AGENTCTL_HOME, TMUX, TMUX_PANE). It turns the payload
// into an observation (adapter/claude.ParseHook), resolves the session by
// explicit identity, and records hook.received plus the caused state
// transition in the registry. With controller it is the only writer of
// session state.
//
// Invariant 7 (hooks fail open): Run never returns an error, never writes to
// stdout (Claude Code feeds SessionStart / UserPromptSubmit hook stdout into
// the model's context), finishes within Budget, and appends every error to
// <home>/logs/hook-errors.log.
//
// Pipeline (one file per stage): read stdin (≤ MaxPayload) → ParseHook →
// open the registry (store.HookBusyTimeout) → resolve the session
// (resolve.go) or record hook.orphan → store.Sessions.RecordHook → followups
// (followup.go: the seam where m2-prompt-queue adds ack matching and the
// deliver trigger).
package hookingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
)

// Limits. DefaultBudget is controller.Tunables.HookBudget; hookingest keeps
// its own copy because it must not import controller.
const (
	DefaultBudget = 1800 * time.Millisecond
	MaxPayload    = 1 << 20 // bytes of stdin read; more is an error
)

// Env variable names the hook reads.
const (
	EnvTaskID   = "AGENTCTL_TASK_ID"
	EnvTMUX     = "TMUX"
	EnvTMUXPane = "TMUX_PANE"
)

// Config wires one hook run. Only Getenv and Stdin are required.
type Config struct {
	Getenv      func(string) string // environment lookup (the hook's inherited env)
	Stdin       io.Reader           // the hook payload
	Stderr      io.Writer           // last resort when logs/hook-errors.log cannot be written (default io.Discard)
	Executable  string              // absolute agentctl path (default Spawner target)
	Budget      time.Duration       // self-timeout (default DefaultBudget)
	BusyTimeout time.Duration       // SQLite busy timeout (default store.HookBusyTimeout)
	Now         func() time.Time    // default time.Now
	Spawner     Spawner             // starts detached `agentctl deliver` (default ExecSpawner)
	PID         int                 // this process's pid, for deliver --after-pid (default os.Getpid())

	followups []followupEntry // tests replace the registry
}

// Outcome summarizes a run for tests; `agentctl hook` ignores it and exits 0.
type Outcome struct {
	Event     string // hook_event_name ("" if the payload was unreadable)
	TaskID    string // resolved task
	SessionID int64  // resolved session (0 for orphans and unmanaged panes)
	Orphan    string // hook.orphan reason, "" when matched or ignored
	Applied   bool   // a state write happened
	TimedOut  bool   // the budget elapsed before the pipeline finished
	Err       error  // first error (already logged)
}

// Run executes the pipeline under the self-timeout. It always returns; every
// error is logged, and the returned Outcome is informational.
func Run(ctx context.Context, cfg Config) Outcome {
	cfg = cfg.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, cfg.Budget)
	defer cancel()
	r := &runner{cfg: cfg, log: &errorLog{stderr: cfg.Stderr, now: cfg.Now, pid: cfg.PID}}
	done := make(chan Outcome, 1)
	go func() {
		var o Outcome
		defer func() {
			if p := recover(); p != nil {
				o.Err = fmt.Errorf("hook panicked: %v", p)
				r.log.write(o, o.Err)
			}
			done <- o
		}()
		o = r.run(ctx)
	}()
	select {
	case o := <-done:
		return o
	case <-ctx.Done():
		o := r.snapshot()
		o.TimedOut = true
		o.Err = fmt.Errorf("hook budget %v exceeded; gave up (state not recorded)", cfg.Budget)
		r.log.write(o, o.Err)
		r.lostOnTimeout(o) // lost.go: the event may never be recorded
		return o
	}
}

func (c Config) withDefaults() Config {
	if c.Getenv == nil {
		c.Getenv = func(string) string { return "" }
	}
	if c.Stdin == nil {
		c.Stdin = eofReader{}
	}
	if c.Stderr == nil {
		c.Stderr = io.Discard
	}
	if c.Budget <= 0 {
		c.Budget = DefaultBudget
	}
	if c.BusyTimeout <= 0 {
		c.BusyTimeout = store.HookBusyTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.PID == 0 {
		c.PID = os.Getpid()
	}
	if c.followups == nil {
		c.followups = registered()
	}
	return c
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// runner carries one run's state. progress is written only by the pipeline
// goroutine before each stage and read by Run on timeout under the log mutex.
type runner struct {
	cfg      Config
	log      *errorLog
	progress Outcome
	lost     lostState // lost.go; guarded by log.mu like progress
}

func (r *runner) snapshot() Outcome {
	r.log.mu.Lock()
	defer r.log.mu.Unlock()
	return r.progress
}

func (r *runner) note(o Outcome) {
	r.log.mu.Lock()
	r.progress = o
	r.log.mu.Unlock()
}

func (r *runner) fail(o Outcome, err error) Outcome {
	o.Err = err
	r.log.write(o, err)
	return o
}

func (r *runner) run(ctx context.Context) Outcome {
	var o Outcome
	env := adapter.HookEnv{
		TaskID: r.cfg.Getenv(EnvTaskID), Home: r.cfg.Getenv(paths.EnvHome),
		TMUX: r.cfg.Getenv(EnvTMUX), TMUXPane: r.cfg.Getenv(EnvTMUXPane),
	}
	o.TaskID = env.TaskID
	p, perr := paths.Resolve(r.cfg.Getenv)
	r.setLostContext(env, p)
	if perr == nil {
		if fi, err := os.Stat(p.Home); err == nil && fi.IsDir() {
			r.log.setPath(filepath.Join(p.Logs, ErrorLogName))
		} else {
			perr = fmt.Errorf("agentctl home %s does not exist", p.Home)
		}
	}
	r.note(o)

	payload, err := readPayload(r.cfg.Stdin)
	managed := env.TaskID != "" || env.TMUXPane != ""
	if !managed {
		return o // not an agentctl pane (a user-wide hooks-snippet firing elsewhere): nothing to do
	}
	if err != nil {
		return r.failLost(o, err)
	}
	obs, err := claude.New(claude.Config{}).ParseHook(payload, env)
	if err != nil {
		return r.failLost(o, err)
	}
	o.Event = obs.Event
	r.note(o)
	if perr != nil {
		return r.unregistered(o, env, perr)
	}
	if _, err := os.Stat(p.DB); err != nil {
		return r.unregistered(o, env, fmt.Errorf("registry %s not found", p.DB))
	}
	st, err := store.Open(ctx, p.DB, store.Options{BusyTimeout: r.cfg.BusyTimeout, Now: r.cfg.Now})
	if err != nil {
		return r.failLost(o, err)
	}
	defer st.Close()

	match, err := resolve(ctx, st, env)
	if err != nil {
		return r.failLost(o, err)
	}
	switch {
	case match.orphan != "":
		o.Orphan = match.orphan
		if err := recordOrphan(ctx, st, env, obs, match.orphan); err != nil {
			return r.fail(o, err)
		}
		return o
	case match.session.ID == 0:
		return o // unmanaged pane
	}
	o.TaskID, o.SessionID = match.session.TaskID, match.session.ID
	r.note(o)
	in := store.HookInput{
		SessionID: match.session.ID, Event: obs.Event, AgentSessionID: obs.AgentSessionID,
		State: obs.State, Detail: obs.Detail, Payload: receivedPayload(obs, match.how),
	}
	res, err := retryFastBusy(ctx, func() (store.HookResult, error) { return st.Sessions().RecordHook(ctx, in) })
	if err != nil {
		return r.failLost(o, err)
	}
	o.Applied = res.Applied
	r.note(o)
	r.recorded(o, p, obs, res) // lost.go: clears this session's lost-hook markers
	r.runFollowups(ctx, st, Applied{Observation: obs, Session: res.Session, Result: res, Match: match.how}, o)
	return o
}

// unregistered handles a hook that names a task but has no registry to
// record into. It never creates the home or the database.
func (r *runner) unregistered(o Outcome, env adapter.HookEnv, err error) Outcome {
	if env.TaskID == "" {
		return o // an unmanaged pane on a machine without agentctl state
	}
	return r.fail(o, err)
}

func readPayload(in io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(in, MaxPayload+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("read hook payload: %w", err)
	case len(b) > MaxPayload:
		return nil, fmt.Errorf("hook payload exceeds %d bytes; ignored", MaxPayload)
	case len(b) == 0:
		return nil, errors.New("empty hook payload on stdin")
	}
	return b, nil
}

// receivedPayload is the hook.received payload: the bounded attrs, the prompt
// hash (never the prompt), and how the session was matched.
func receivedPayload(obs adapter.HookObservation, how string) map[string]any {
	p := map[string]any{"match": how}
	for k, v := range obs.Attrs {
		p[k] = v
	}
	if obs.PromptSHA256 != "" {
		p["prompt_sha256"] = obs.PromptSHA256
	}
	if obs.AgentSessionID != "" {
		p["agent_session_id"] = obs.AgentSessionID
	}
	return p
}

// SettingsSnippet is what `agentctl hooks-snippet` prints: exactly the hook
// settings `agentctl new` writes, for users to merge into their own Claude
// settings so adopted panes report state.
func SettingsSnippet(agentctlPath string) ([]byte, error) {
	return claude.SettingsJSON(agentctlPath)
}
