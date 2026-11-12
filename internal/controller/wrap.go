package controller

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
)

// WrapRequest is `agentctl codex [--name <name>] [codex args…]` run in the
// tmux pane PaneID ($TMUX_PANE).
type WrapRequest struct {
	Name     string   // task name; "" = derived from the pane's worktree (<dir>-codex)
	PaneID   string   // the pane agentctl codex runs in
	UserArgs []string // passed to codex unchanged, after agentctl's hook flags
}

// WrapResult says what was registered and how to start Codex.
type WrapResult struct {
	TaskID    string
	SessionID int64
	Identity  domain.Identity
	Reused    bool     // the pane's task existed (its previous session had ended)
	Argv      []string // codex argv: resolved codex command + hook flags + UserArgs
	Env       []string // extra environment for codex: AGENTCTL_TASK_ID, AGENTCTL_HOME
}

// RegisterWrapped registers the user's own pane as a Codex task (origin
// wrapped, agent codex) and returns the argv to run there. The identity is
// the live pane's (its pane pid is the user's shell, so it survives Codex
// restarts in the same pane). The session starts starting / none with
// detail DetailAwaitingFirstPrompt: Codex fires no hook before the first
// prompt, so the startup hook-trust check runs and the session sits in
// INITIALSTATE until then (expected silence; never timed out).
//
// A pane already bound to a task whose latest session ended (stopped,
// failed, disconnected) gets a new session on that task (Reused; Name is
// ignored). A pane bound to a live session is refused with ErrPaneRegistered.
func (c *Controller) RegisterWrapped(ctx context.Context, req WrapRequest) (WrapResult, error) {
	if !isPaneID(req.PaneID) {
		return WrapResult{}, userErr(ErrPaneNotFound, nil, "no tmux pane id (%q); agentctl codex must run inside tmux", req.PaneID)
	}
	if req.Name != "" {
		if err := domain.ValidateSlug(req.Name); err != nil {
			return WrapResult{}, userErr(ErrInvalidName, err, "%v", err)
		}
	}
	for _, a := range req.UserArgs {
		if strings.IndexByte(a, 0) >= 0 {
			return WrapResult{}, userErr(ErrAgentCmdInvalid, nil, "a codex argument contains a NUL byte")
		}
	}
	if err := c.requireTmux(); err != nil {
		return WrapResult{}, err
	}
	tl, err := c.requireTools(codex.Name)
	if err != nil {
		return WrapResult{}, err
	}
	pane, id, err := c.adoptablePane(ctx, req.PaneID)
	if errors.Is(err, ErrPaneRegistered) {
		return c.rewrap(ctx, req, tl.agentCmd)
	}
	if err != nil {
		return WrapResult{}, err
	}
	loc, err := c.paneLocation(ctx, pane)
	if err != nil {
		return WrapResult{}, err
	}
	name := req.Name
	if name == "" {
		if name, err = c.wrapName(ctx, loc.worktree); err != nil {
			return WrapResult{}, err
		}
	} else if exists, err := c.store.Tasks().Exists(ctx, name); err != nil {
		return WrapResult{}, err
	} else if exists {
		return WrapResult{}, taskExists(name)
	}
	repoRow, err := c.store.Repos().Upsert(ctx, loc.repo.Root, loc.repo.Name)
	if err != nil {
		return WrapResult{}, err
	}
	task := store.Task{
		ID: name, RepoID: repoRow.ID, Name: name, WorktreePath: loc.worktree,
		Branch: loc.head.Ref, BaseRef: loc.baseRef, BaseSHA: loc.baseSHA,
	}
	_, sess, err := c.store.Sessions().RegisterAdopted(ctx, task, c.wrappedSession(name, id, pane.SessionName, loc.worktree,
		store.EventInput{Type: domain.EventTaskCreated, Payload: map[string]any{
			"repo": loc.repo.Name, "repo_root": loc.repo.Root, "branch": loc.head.Ref, "worktree": loc.worktree,
			"base_ref": loc.baseRef, "base_sha": loc.baseSHA, "origin": string(domain.OriginWrapped),
		}}))
	switch {
	case errors.Is(err, store.ErrTaskExists):
		return WrapResult{}, taskExists(name)
	case errors.Is(err, store.ErrPaneRegistered):
		return WrapResult{}, userErr(ErrPaneRegistered, err, "pane %s is already registered (`agentctl ls --closed` shows its task)", req.PaneID)
	case err != nil:
		return WrapResult{}, err
	}
	return c.wrapResult(name, sess, false, tl.agentCmd, req.UserArgs)
}

// rewrap starts a new session on the task that owns the pane, when that
// task's latest session has ended.
func (c *Controller) rewrap(ctx context.Context, req WrapRequest, codexCmd []string) (WrapResult, error) {
	pane, id, err := c.livePane(ctx, req.PaneID)
	if err != nil {
		return WrapResult{}, err
	}
	owner, err := c.store.Sessions().PaneOwner(ctx, id)
	if err != nil {
		return WrapResult{}, err
	}
	latest, err := c.store.Sessions().LatestForTask(ctx, owner.TaskID)
	if err != nil {
		return WrapResult{}, err
	}
	if !latest.State.IsTerminal() {
		return WrapResult{}, userErr(ErrPaneRegistered, nil, "this pane already runs agentctl task %q (%s); exit it first", owner.TaskID, latest.State)
	}
	if latest.Origin != domain.OriginWrapped || latest.AgentType != codex.Name {
		return WrapResult{}, userErr(ErrPaneRegistered, nil, "pane %s belongs to task %q, which agentctl codex did not start", req.PaneID, owner.TaskID)
	}
	task, err := c.store.Tasks().Get(ctx, owner.TaskID)
	if err != nil {
		return WrapResult{}, err
	}
	if task.State == domain.TaskClosed {
		return WrapResult{}, userErr(ErrPaneRegistered, nil, "this pane belongs to closed task %q; run agentctl codex in a new pane", task.ID)
	}
	sess, err := c.store.Sessions().Create(ctx, c.wrappedSession(task.ID, id, pane.SessionName, task.WorktreePath))
	if err != nil {
		return WrapResult{}, err
	}
	return c.wrapResult(task.ID, sess, true, codexCmd, req.UserArgs)
}

// wrappedSession is the session row of a wrapped pane (plus extra events
// before session.wrapped).
func (c *Controller) wrappedSession(taskID string, id domain.Identity, tmuxSession, worktree string, before ...store.EventInput) store.NewSession {
	return store.NewSession{
		TaskID: taskID, AgentType: codex.Name, Origin: domain.OriginWrapped, Identity: id,
		SessionName: tmuxSession, State: domain.StateStarting, Confidence: domain.ConfidenceNone,
		Detail: domain.DetailAwaitingFirstPrompt,
		Events: append(before, store.EventInput{Type: domain.EventSessionWrapped, Payload: map[string]any{
			"socket": id.Socket, "server_pid": id.ServerPID, "session_id": id.SessionID,
			"session_name": domain.SanitizeTerminalText(tmuxSession), "pane_id": id.PaneID, "pane_pid": id.PanePID,
			"agent_type": codex.Name, "worktree": worktree,
		}}),
	}
}

func (c *Controller) wrapResult(taskID string, sess store.Session, reused bool, codexCmd, userArgs []string) (WrapResult, error) {
	argv, err := codex.LaunchArgv(codexCmd, c.agentctlPath, "")
	if err != nil {
		return WrapResult{}, err
	}
	return WrapResult{
		TaskID: taskID, SessionID: sess.ID, Identity: sess.Identity, Reused: reused,
		Argv: append(argv, userArgs...),
		Env:  []string{"AGENTCTL_TASK_ID=" + taskID, paths.EnvHome + "=" + c.paths.Home},
	}, nil
}

// wrapName is <worktree dir>-codex, then -2, -3, … until unused.
func (c *Controller) wrapName(ctx context.Context, worktree string) (string, error) {
	base := domain.SlugFrom(filepath.Base(worktree))
	const suffix = "-codex"
	if base == "" {
		base = "codex"
	} else {
		if len(base) > domain.MaxSlugLen-len(suffix)-3 {
			base = strings.TrimRight(base[:domain.MaxSlugLen-len(suffix)-3], "-")
		}
		base += suffix
	}
	for n := 1; n < 1000; n++ {
		name := base
		if n > 1 {
			name = base + "-" + strconv.Itoa(n)
		}
		exists, err := c.store.Tasks().Exists(ctx, name)
		if err != nil {
			return "", err
		}
		if !exists {
			return name, nil
		}
	}
	return "", userErr(ErrTaskExists, nil, "no free task name like %q (pass --name)", base)
}

// RecordWrappedExit records how Codex exited in a wrapped pane: exit 0 →
// stopped, anything else → failed, detail "codex exited (code N)", source
// wrapper. The wrapper must record it because the pane outlives Codex (its
// pane pid is the user's shell), so reconcile sees a live pane. A session
// already terminal is left alone. Only wrapped sessions are accepted.
func (c *Controller) RecordWrappedExit(ctx context.Context, sessionID int64, exitCode int) error {
	sess, err := c.store.Sessions().Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.Origin != domain.OriginWrapped {
		return fmt.Errorf("session %d was not started by agentctl codex (origin %s)", sessionID, sess.Origin)
	}
	if sess.State.IsTerminal() {
		return nil
	}
	to := domain.StateStopped
	if exitCode != 0 {
		to = domain.StateFailed
	}
	_, err = c.store.Sessions().ReconcileTransition(ctx, store.ReconcileInput{
		SessionID: sessionID, To: to, Confidence: domain.ConfidencePane,
		Detail: "codex exited (code " + strconv.Itoa(exitCode) + ")", Source: domain.SourceWrapper,
	})
	return err
}

// wrapperForeground reports whether the foreground process group of the
// pane whose process is panePID contains `agentctl codex` (the wrapper,
// which lives exactly as long as Codex does). tmux names a pane after its
// foreground group's leader, which under the wrapper is agentctl, not codex,
// so the process table is the reliable check. Any failure is "no": a paste
// is only allowed on proof (invariant 3).
func (c *Controller) wrapperForeground(ctx context.Context, panePID int) bool {
	procs, err := c.procs(ctx)
	if err != nil {
		return false
	}
	self := filepath.Base(c.agentctlPath)
	for _, p := range newProcTable(procs).foreground(panePID) {
		argv := strings.Fields(p.Args)
		if len(argv) >= 2 && argv[1] == "codex" {
			if b := filepath.Base(argv[0]); b == self || b == "agentctl" {
				return true
			}
		}
	}
	return false
}
