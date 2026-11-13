package controller

import (
	"context"
	"errors"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// Close error kinds (errors.Is).
var (
	ErrTaskClosed  = errors.New("task already closed")
	ErrStopRefused = errors.New("stop refused")
)

// stopDetail is the session detail recorded by a confirmed close --stop.
const stopDetail = "stopped by user (close --stop)"

// CloseRequest is `agentctl close <name> [--stop]`.
type CloseRequest struct {
	Name string
	Stop bool // also kill the tmux session (launched + verified sessions only)
}

// ClosePlan is what a close would do; the CLI and TUI show it in the
// confirmation prompt before a stop.
type ClosePlan struct {
	TaskID      string
	Worktree    string
	Branch      string
	Stop        bool
	HasSession  bool
	SessionName string // sanitize before display
	Identity    domain.Identity
	Origin      domain.Origin
	AgentState  domain.AgentState
	Liveness    domain.Liveness // verified identity; set only when Stop
}

// CloseResult reports a completed close.
type CloseResult struct {
	ClosePlan
	Stopped bool // the tmux session was killed
}

// PlanClose validates a close without changing anything: the task must exist
// and be running; with Stop, its newest session must have been launched by
// agentctl (never adopted), must not be recorded as disconnected, and its
// pane identity must verify right now (alive or dead). Refusals are one-line
// *Errors of kind ErrStopRefused.
func (c *Controller) PlanClose(ctx context.Context, req CloseRequest) (ClosePlan, error) {
	plan, _, err := c.planClose(ctx, req)
	return plan, err
}

// CloseTask closes a task: it is hidden from `ls` (shown with --closed) and
// its worktree, branch and session are left alone (invariants 2 and 9).
// With Stop, which callers only pass after explicit confirmation, it
// re-validates as PlanClose, records a dead pane's exit status first, kills
// the tmux session through the identity-guarded adapter Stop, and records
// the session as stopped (source user) atomically with the close.
func (c *Controller) CloseTask(ctx context.Context, req CloseRequest) (CloseResult, error) {
	plan, sess, err := c.planClose(ctx, req)
	if err != nil {
		return CloseResult{ClosePlan: plan}, err
	}
	in := store.CloseInput{TaskID: plan.TaskID}
	if req.Stop {
		if plan.Liveness.Status == domain.LivenessDead {
			if err := c.reconcileSession(ctx, sess, plan.Liveness); err != nil {
				return CloseResult{ClosePlan: plan}, err
			}
		}
		h := adapter.Handle{Identity: sess.Identity, SessionName: sess.SessionName}
		agent, err := c.agentFor(sess.AgentType)
		if err != nil {
			return CloseResult{ClosePlan: plan}, err
		}
		// a pane docked beside a dashboard sits in that dashboard's session;
		// the kill's identity guard needs it back in its own
		if err := c.undockPane(ctx, sess.Identity); err != nil {
			return CloseResult{ClosePlan: plan}, userErr(ErrStopRefused, err, "refusing to stop %q: cannot move its pane back from the dashboard: %v; nothing was killed and the task is still open", plan.TaskID, err)
		}
		if err := agent.Stop(ctx, h); err != nil {
			if errors.Is(err, claude.ErrStopRefused) {
				return CloseResult{ClosePlan: plan}, userErr(ErrStopRefused, err, "refusing to stop %q: %v; nothing was killed and the task is still open", plan.TaskID, err)
			}
			return CloseResult{ClosePlan: plan}, userErr(ErrStopRefused, err, "stopping %q failed: %v; the task is still open", plan.TaskID, err)
		}
		in.StopSessionID, in.StopDetail = sess.ID, stopDetail
	}
	if _, err := c.store.Tasks().Close(ctx, in); err != nil {
		if errors.Is(err, store.ErrTaskClosed) {
			return CloseResult{ClosePlan: plan}, taskClosed(plan.TaskID)
		}
		return CloseResult{ClosePlan: plan}, err
	}
	return CloseResult{ClosePlan: plan, Stopped: req.Stop}, nil
}

func (c *Controller) planClose(ctx context.Context, req CloseRequest) (ClosePlan, store.Session, error) {
	task, err := c.store.Tasks().Get(ctx, req.Name)
	if errors.Is(err, store.ErrNotFound) {
		return ClosePlan{}, store.Session{}, userErr(ErrTaskNotFound, err, "no task named %q (`agentctl ls --closed` lists every task)", req.Name)
	}
	if err != nil {
		return ClosePlan{}, store.Session{}, err
	}
	plan := ClosePlan{TaskID: task.ID, Worktree: task.WorktreePath, Branch: task.Branch, Stop: req.Stop}
	if task.State == domain.TaskClosed {
		return plan, store.Session{}, taskClosed(task.ID)
	}
	sess, err := c.store.Sessions().LatestForTask(ctx, task.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return plan, store.Session{}, err
	default:
		plan.HasSession, plan.SessionName, plan.Identity = true, sess.SessionName, sess.Identity
		plan.Origin, plan.AgentState = sess.Origin, sess.State
	}
	if !req.Stop {
		return plan, sess, nil
	}
	if err := c.checkStoppable(ctx, &plan, sess); err != nil {
		return plan, sess, err
	}
	return plan, sess, nil
}

// checkStoppable applies the close --stop rules and fills plan.Liveness.
func (c *Controller) checkStoppable(ctx context.Context, plan *ClosePlan, sess store.Session) error {
	id := plan.TaskID
	switch {
	case !plan.HasSession:
		return userErr(ErrStopRefused, nil, "task %q has no agent session to stop (`agentctl close %s` hides it)", id, id)
	case sess.Origin != domain.OriginLaunched:
		return userErr(ErrStopRefused, nil, "refusing to stop %q: its session was adopted, not launched by agentctl; stop the agent yourself (`agentctl close %s` only hides the task)", id, id)
	case sess.State == domain.StateDisconnected:
		return userErr(ErrStopRefused, nil, "refusing to stop %q: its pane identity cannot be verified (%s); nothing was killed (`agentctl close %s` hides the task)", id, sess.LastDetail, id)
	}
	l, _ := c.liveSession(ctx, sess)
	plan.Liveness = l
	if !l.IsVerified() {
		return userErr(ErrStopRefused, nil, "refusing to stop %q: its pane identity cannot be verified (%s); nothing was killed (`agentctl close %s` hides the task)", id, l, id)
	}
	return nil
}

func taskClosed(id string) error {
	return userErr(ErrTaskClosed, nil, "task %q is already closed (`agentctl ls --closed` shows it)", id)
}
