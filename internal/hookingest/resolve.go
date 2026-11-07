package hookingest

import (
	"context"
	"errors"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// How a hook was matched to its session (hook.received payload "match").
const (
	MatchTaskEnv = "task_env" // AGENTCTL_TASK_ID + verified pane identity (launched sessions)
	MatchPane    = "pane"     // pane identity only (adopted pane with a merged hooks-snippet)
)

// Orphan reasons (hook.orphan payload "reason"). An orphan changes nothing.
const (
	OrphanInvalidTaskID       = "invalid_task_id"
	OrphanUnknownTask         = "unknown_task"
	OrphanNoTmuxEnv           = "no_tmux_env"
	OrphanAmbiguousPane       = "ambiguous_pane"
	OrphanLaunchedWithoutTask = "launched_without_task_env"
	// plus claude.HookPane.Matches reasons: pane_mismatch, server_pid_mismatch,
	// socket_mismatch, session_has_no_identity
)

// match is the resolution of one hook. Exactly one of: session.ID != 0
// (matched), orphan != "" (record hook.orphan), or neither (unmanaged pane:
// record nothing).
type match struct {
	session store.Session
	how     string
	orphan  string
}

// resolve binds a hook to a session by explicit identity (invariant 5); it
// never infers a task from cwd or command name.
//
// With AGENTCTL_TASK_ID: the task's latest session, and $TMUX (socket, server
// pid) plus $TMUX_PANE must match its identity. A pane reused after a tmux
// server restart has the same %N but a new server pid, so it is an orphan.
//
// Without it (an adopted pane whose user merged `agentctl hooks-snippet`):
// the single active adopted session with that exact pane identity. A pane no
// session knows is not agentctl's business and records nothing; a pane id
// that some session knows but whose socket/pid differ is an orphan.
func resolve(ctx context.Context, st *store.Store, env adapter.HookEnv) (match, error) {
	if env.TaskID != "" {
		return resolveTask(ctx, st, env)
	}
	pane, err := claude.PaneFromEnv(env)
	if err != nil {
		return match{}, nil // not in tmux: nothing to match
	}
	cands, err := st.Sessions().ActiveByPane(ctx, pane.PaneID)
	if err != nil {
		return match{}, err
	}
	var hits []store.Session
	firstMiss := ""
	for _, s := range cands {
		ok, reason := pane.Matches(s.Identity)
		switch {
		case !ok:
			if firstMiss == "" {
				firstMiss = reason
			}
		case s.Origin != domain.OriginAdopted:
			if firstMiss == "" {
				firstMiss = OrphanLaunchedWithoutTask
			}
		default:
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 0:
		return match{orphan: firstMiss}, nil // firstMiss == "" -> unmanaged pane
	case 1:
		return match{session: hits[0], how: MatchPane}, nil
	}
	return match{orphan: OrphanAmbiguousPane}, nil
}

func resolveTask(ctx context.Context, st *store.Store, env adapter.HookEnv) (match, error) {
	if domain.ValidateSlug(env.TaskID) != nil {
		return match{orphan: OrphanInvalidTaskID}, nil
	}
	sess, err := st.Sessions().LatestForTask(ctx, env.TaskID)
	if errors.Is(err, store.ErrNotFound) {
		return match{orphan: OrphanUnknownTask}, nil
	}
	if err != nil {
		return match{}, err
	}
	pane, err := claude.PaneFromEnv(env)
	if err != nil {
		return match{orphan: OrphanNoTmuxEnv}, nil
	}
	if ok, reason := pane.Matches(sess.Identity); !ok {
		return match{orphan: reason}, nil
	}
	return match{session: sess, how: MatchTaskEnv}, nil
}

// recordOrphan appends hook.orphan. It carries no task/session columns: the
// hook could not be attributed, and a claimed task id is only a claim.
func recordOrphan(ctx context.Context, st *store.Store, env adapter.HookEnv, obs adapter.HookObservation, reason string) error {
	p := map[string]any{"event": obs.Event, "reason": reason}
	if env.TaskID != "" && domain.ValidateSlug(env.TaskID) == nil {
		p["claimed_task"] = env.TaskID
	}
	if pane, err := claude.PaneFromEnv(env); err == nil {
		p["tmux_pane"] = pane.PaneID
		p["tmux_server_pid"] = pane.ServerPID
	}
	_, err := st.Events().Append(ctx, store.EventInput{Type: domain.EventHookOrphan, Payload: p})
	return err
}
