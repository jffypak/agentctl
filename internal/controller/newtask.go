package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/adapter/codex"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/gitx"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/tmux"
)

// MaxGoalLen bounds the goal (it becomes one argv element).
const MaxGoalLen = 64 << 10

// BranchPrefix prefixes every task branch.
const BranchPrefix = "jpark/"

// NewTaskRequest is `agentctl new <name> --goal <goal>` run in Cwd.
type NewTaskRequest struct {
	Name string
	Goal string
	Cwd  string // any directory inside the repo or one of its worktrees
	// Agent is the agent to launch: "" or "claude" (default), or "codex".
	Agent string
}

// NewTaskResult describes the launched (or launch-failed) task.
type NewTaskResult struct {
	TaskID      string
	Repo        string
	RepoRoot    string
	Branch      string
	Worktree    string
	BaseRef     string
	BaseSHA     string
	SessionID   int64
	SessionName string
	Identity    domain.Identity
	State       domain.AgentState // starting, or failed when the launch failed
	Detail      string
}

// NewTask creates a worktree on jpark/<name> from the cwd's HEAD and
// launches the agent in tmux session agentctl-<name>.
//
// Every check that can fail runs before anything is written, so the listed
// errors leave no partial row. The task row is then reserved (the
// serialization point for concurrent `new` of one name), the worktree is
// added (failure undoes the reservation), and the agent is started. If tmux
// fails after the worktree exists, the task and worktree are kept and the
// session is recorded as failed with the tmux error as its detail.
func (c *Controller) NewTask(ctx context.Context, req NewTaskRequest) (NewTaskResult, error) {
	name := req.Name
	if !filepath.IsAbs(req.Cwd) {
		// never fall back to the process cwd: that could be any repository
		return NewTaskResult{}, userErr(ErrNotARepo, nil, "no absolute working directory given (%q); run agentctl new from inside a git repository", req.Cwd)
	}
	if err := domain.ValidateSlug(name); err != nil {
		return NewTaskResult{}, userErr(ErrInvalidName, err, "%v", err)
	}
	if err := validateGoal(req.Goal); err != nil {
		return NewTaskResult{}, err
	}
	agent, err := c.agentFor(req.Agent)
	if err != nil {
		return NewTaskResult{}, err
	}
	tl, err := c.requireTools(agent.Name())
	if err != nil {
		return NewTaskResult{}, err
	}
	repo, err := c.git.MainRepo(ctx, req.Cwd)
	if err != nil {
		if errors.Is(err, gitx.ErrNotARepo) {
			return NewTaskResult{}, userErr(ErrNotARepo, err, "%s is not inside a git repository (cd into a repo and retry)", req.Cwd)
		}
		return NewTaskResult{}, userErr(ErrNotARepo, err, "cannot resolve the git repository for %s: %v", req.Cwd, err)
	}
	if exists, err := c.store.Tasks().Exists(ctx, name); err != nil {
		return NewTaskResult{}, err
	} else if exists {
		return NewTaskResult{}, taskExists(name)
	}
	head, err := c.git.Head(ctx, req.Cwd)
	if err != nil {
		if errors.Is(err, gitx.ErrNoCommits) {
			return NewTaskResult{}, userErr(ErrNoCommits, err, "%s has no commits yet (make an initial commit first)", repo.Root)
		}
		return NewTaskResult{}, err
	}
	branch := BranchPrefix + name
	worktree := filepath.Join(filepath.Dir(repo.Root), repo.Name+"-worktrees", name)
	if ok, err := c.git.BranchExists(ctx, repo.Root, branch); err != nil {
		return NewTaskResult{}, err
	} else if ok {
		return NewTaskResult{}, branchExists(branch, repo.Root)
	}
	if _, err := os.Lstat(worktree); err == nil {
		return NewTaskResult{}, worktreeExists(worktree)
	}
	socket, err := tmux.ResolveSocket(c.Getenv, c.uid)
	if err != nil {
		return NewTaskResult{}, userErr(ErrLaunchFailed, err, "%v", err)
	}
	sessionName := claude.SessionName(name)
	if has, _ := c.tmuxClient(socket).HasSession(ctx, sessionName); has {
		return NewTaskResult{}, userErr(ErrSessionExists, nil, "tmux session %s already exists on %s (pick another name)", sessionName, socket)
	}

	repoRow, err := c.store.Repos().Upsert(ctx, repo.Root, repo.Name)
	if err != nil {
		return NewTaskResult{}, err
	}
	task := store.Task{
		ID: name, RepoID: repoRow.ID, Name: name, Goal: req.Goal,
		WorktreePath: worktree, Branch: branch, BaseRef: head.Ref, BaseSHA: head.SHA,
	}
	if _, err := c.store.Tasks().Reserve(ctx, task); err != nil {
		if errors.Is(err, store.ErrTaskExists) {
			return NewTaskResult{}, taskExists(name)
		}
		return NewTaskResult{}, err
	}
	if err := c.git.AddWorktree(ctx, repo.Root, worktree, branch, head.SHA); err != nil {
		if uerr := c.store.Tasks().Unreserve(ctx, name); uerr != nil {
			return NewTaskResult{}, userErr(ErrWorktreeFailed, err, "git worktree add failed (%v) and the task reservation could not be undone (%v)", err, uerr)
		}
		switch {
		case errors.Is(err, gitx.ErrBranchExists):
			return NewTaskResult{}, branchExists(branch, repo.Root)
		case errors.Is(err, gitx.ErrWorktreePathExists):
			return NewTaskResult{}, worktreeExists(worktree)
		}
		return NewTaskResult{}, userErr(ErrWorktreeFailed, err, "git worktree add failed: %v", err)
	}

	res := NewTaskResult{
		TaskID: name, Repo: repo.Name, RepoRoot: repo.Root, Branch: branch, Worktree: worktree,
		BaseRef: head.Ref, BaseSHA: head.SHA, SessionName: sessionName,
	}
	createdEv := store.EventInput{Type: domain.EventTaskCreated, Payload: map[string]any{
		"repo": repo.Name, "repo_root": repo.Root, "branch": branch, "worktree": worktree,
		"base_ref": head.Ref, "base_sha": head.SHA, "origin": string(domain.OriginLaunched),
	}}
	h, startErr := agent.Start(ctx, adapter.LaunchSpec{
		TaskID: name, Goal: req.Goal, Worktree: worktree, Home: c.paths.Home, Socket: socket, AgentCmd: tl.agentCmd,
	})
	if startErr != nil {
		return c.recordLaunchFailure(ctx, agent.Name(), res, createdEv, startErr)
	}
	sess, err := c.store.Sessions().Create(ctx, store.NewSession{
		TaskID: name, AgentType: agent.Name(), Origin: domain.OriginLaunched,
		Identity: h.Identity, SessionName: h.SessionName,
		State: domain.StateStarting, Confidence: domain.ConfidenceNone, Detail: launchDetail(agent.Name(), req.Goal),
		Events: []store.EventInput{createdEv, {Type: domain.EventSessionStarted, Payload: map[string]any{
			"socket": h.Identity.Socket, "server_pid": h.Identity.ServerPID, "session_id": h.Identity.SessionID,
			"session_name": h.SessionName, "pane_id": h.Identity.PaneID, "pane_pid": h.Identity.PanePID,
			"agent_type": agent.Name(), "argv0": h.Argv[0],
		}}},
	})
	if err != nil {
		return res, userErr(ErrLaunchFailed, err, "agent started in tmux session %s (%s) but recording it failed: %v", h.SessionName, h.Identity.PaneID, err)
	}
	res.SessionID, res.Identity, res.State = sess.ID, sess.Identity, sess.State
	return res, nil
}

func (c *Controller) recordLaunchFailure(ctx context.Context, agentType string, res NewTaskResult, createdEv store.EventInput, startErr error) (NewTaskResult, error) {
	detail := "launch failed: " + OneLine(startErr.Error())
	sess, err := c.store.Sessions().Create(ctx, store.NewSession{
		TaskID: res.TaskID, AgentType: agentType, Origin: domain.OriginLaunched,
		SessionName: res.SessionName, State: domain.StateFailed, Confidence: domain.ConfidenceNone, Detail: detail,
		Events: []store.EventInput{createdEv, {Type: domain.EventSessionLaunchFailed, Payload: map[string]any{"error": OneLine(startErr.Error())}}},
	})
	if err != nil {
		return res, userErr(ErrLaunchFailed, startErr, "task %s: agent failed to start (%v) and recording the failure failed (%v); worktree kept at %s", res.TaskID, startErr, err, res.Worktree)
	}
	res.SessionID, res.State, res.Detail = sess.ID, sess.State, sess.LastDetail
	return res, userErr(ErrLaunchFailed, startErr, "task %s created but its agent failed to start: %v (worktree kept at %s)", res.TaskID, startErr, res.Worktree)
}

func validateGoal(g string) error {
	switch {
	case len(g) > MaxGoalLen:
		return userErr(ErrInvalidGoal, nil, "goal is %d bytes; the limit is %d", len(g), MaxGoalLen)
	case strings.IndexByte(g, 0) >= 0:
		return userErr(ErrInvalidGoal, nil, "goal contains a NUL byte")
	case strings.HasPrefix(g, "-"):
		return userErr(ErrInvalidGoal, nil, "goal must not start with '-' (the agent would read it as a flag); rephrase it")
	}
	return nil
}

func taskExists(name string) error {
	return userErr(ErrTaskExists, nil, "task %q already exists (pick another name; `agentctl ls --closed` lists every task)", name)
}

func branchExists(branch, root string) error {
	return userErr(ErrBranchExists, nil, "branch %s already exists in %s (pick another task name)", branch, root)
}

func worktreeExists(path string) error {
	return userErr(ErrWorktreeExists, nil, "worktree path %s already exists (pick another task name or move that directory)", path)
}

// launchDetail is a launched session's first detail: a Codex agent without a
// goal fires no hook until the user types (expected silence, INITIALSTATE).
func launchDetail(agentType, goal string) string {
	if agentType == codex.Name && goal == "" {
		return domain.DetailAwaitingFirstPrompt
	}
	return ""
}
