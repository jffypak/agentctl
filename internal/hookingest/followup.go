package hookingest

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
)

// THE SEAM for m2-prompt-queue. After a hook has been recorded for a matched
// session (hook.received appended, the transition applied or refused), every
// registered Followup runs in ascending order, inside the hook's budget. A
// followup that errors or panics is logged and the next one still runs; the
// recorded state is never rolled back.
//
// m2-prompt-queue adds its stages without restructuring this package, from
// its own files:
//
//	// ack.go
//	var _ = registerFollowup(10, "ack", ackFollowup)       // UserPromptSubmit -> acknowledged (hash, else timing)
//	// trigger.go
//	var _ = registerFollowup(20, "trigger", triggerFollowup) // -> ready with queued messages: d.Spawner.SpawnDeliver(id, d.PID)
//
// Applied.Observation.PromptSHA256 carries the UserPromptSubmit prompt hash;
// Applied.Result.ReceivedID is the cause_id for message.* events.

// Followup is one post-record stage.
type Followup func(ctx context.Context, d Deps, a Applied) error

// Deps are what a followup may use.
type Deps struct {
	Store   *store.Store
	Spawner Spawner          // starts detached `agentctl deliver --session N --after-pid PID`
	PID     int              // this hook process (deliver waits for it to exit)
	Now     func() time.Time // the hook's clock
	Paths   paths.Paths
}

// Applied describes what the pipeline recorded for one hook.
type Applied struct {
	Observation adapter.HookObservation // ParseHook's reading (Event, State, PromptSHA256, Attrs, ...)
	Session     store.Session           // the session after the write
	Result      store.HookResult        // ReceivedID, Transition{From, To, Changed}, Applied, Refused
	Match       string                  // MatchTaskEnv or MatchPane
}

type followupEntry struct {
	order int
	name  string
	fn    Followup
}

var (
	followupMu  sync.Mutex
	followupReg []followupEntry
)

// registerFollowup adds a stage; use it from a package-level var so the
// registration happens at init: `var _ = registerFollowup(10, "ack", f)`.
func registerFollowup(order int, name string, fn Followup) bool {
	followupMu.Lock()
	defer followupMu.Unlock()
	followupReg = append(followupReg, followupEntry{order, name, fn})
	return true
}

// registered returns the registry (a copy), possibly empty but never nil.
func registered() []followupEntry {
	followupMu.Lock()
	defer followupMu.Unlock()
	return append([]followupEntry{}, followupReg...)
}

func (r *runner) runFollowups(ctx context.Context, st *store.Store, a Applied, o Outcome) {
	stages := append([]followupEntry(nil), r.cfg.followups...)
	sort.SliceStable(stages, func(i, j int) bool { return stages[i].order < stages[j].order })
	p, _ := paths.Resolve(r.cfg.Getenv)
	spawner := r.cfg.Spawner
	if spawner == nil {
		spawner = ExecSpawner{Exe: r.cfg.Executable, LogPath: filepath.Join(p.Logs, DeliverLogName)}
	}
	d := Deps{Store: st, Spawner: spawner, PID: r.cfg.PID, Now: r.cfg.Now, Paths: p}
	for _, s := range stages {
		if err := runOne(ctx, s, d, a); err != nil {
			r.log.write(o, err)
		}
	}
}

func runOne(ctx context.Context, s followupEntry, d Deps, a Applied) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("followup %s panicked: %v", s.name, p)
		}
	}()
	if err := s.fn(ctx, d, a); err != nil {
		return fmt.Errorf("followup %s: %w", s.name, err)
	}
	return nil
}
