package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/taskctx"
)

// Delete error kinds (errors.Is).
var (
	ErrDeleteRefused = errors.New("delete refused")
	ErrDeleteFailed  = errors.New("delete failed")
)

// DeletePlan is what a delete would do. The CLI and the TUI show it in the
// confirmation prompt; DeleteTask computes it again before it acts.
type DeletePlan struct {
	TaskID   string
	RepoRoot string
	Worktree string
	Branch   string
	BaseRef  string
	Closed   bool // the task was already closed

	// Stop: the agent agentctl launched still runs; its tmux session is
	// killed first.
	Stop        bool
	SessionName string // sanitize before display
	Identity    domain.Identity

	// Owned: `agentctl new` created the worktree and the branch. Only then
	// does a delete remove them; any other task only loses its name.
	Owned           bool
	RemoveWorktree  bool
	WorktreeMissing bool // the directory is gone; only git's record is pruned
	DirtyFiles      int  // uncommitted or untracked files the removal discards
	DeleteBranch    bool
	UnmergedCommits int // commits on Branch that BaseRef lacks; -1 unknown

	// ContextFile is the task's context file when it holds notes
	// (internal/taskctx); the delete moves it to ContextArchiveDir.
	ContextFile       string
	ContextArchiveDir string

	// Kept lists, one line each, what the delete leaves alone and why.
	Kept []string
}

// DeleteResult reports a completed delete.
type DeleteResult struct {
	DeletePlan
	Stopped         bool
	WorktreeRemoved bool
	BranchDeleted   bool
	ContextArchived string // where the context file went; "" when it held no notes
}

// PlanDelete validates a delete without changing anything.
func (c *Controller) PlanDelete(ctx context.Context, name string) (DeletePlan, error) {
	plan, _, err := c.planDelete(ctx, name)
	return plan, err
}

// DeleteTask removes a task so its name can be used again. Callers pass it
// only after explicit confirmation of PlanDelete. For a task `agentctl new`
// created it kills the agent's tmux session if it still runs, removes the
// worktree (uncommitted changes included) and deletes the branch (merged or
// not). For any task it archives the context file (internal/taskctx), then
// removes agentctl's records. The steps run
// in that order; if one fails, the later ones do not run, the task is kept,
// and a second delete continues where the first one stopped.
func (c *Controller) DeleteTask(ctx context.Context, name string) (DeleteResult, error) {
	plan, sess, err := c.planDelete(ctx, name)
	res := DeleteResult{DeletePlan: plan}
	if err != nil {
		return res, err
	}
	if plan.Stop {
		agent, err := c.agentFor(sess.AgentType)
		if err != nil {
			return res, err
		}
		if err := c.undockPane(ctx, sess.Identity); err != nil {
			return res, userErr(ErrDeleteRefused, err, "refusing to delete %q: cannot move its pane back from the dashboard: %v; nothing was deleted", plan.TaskID, err)
		}
		h := adapter.Handle{Identity: sess.Identity, SessionName: sess.SessionName}
		if err := agent.Stop(ctx, h); err != nil {
			if errors.Is(err, claude.ErrStopRefused) {
				return res, userErr(ErrDeleteRefused, err, "refusing to delete %q: %v; nothing was deleted", plan.TaskID, err)
			}
			return res, userErr(ErrDeleteFailed, err, "stopping %q failed: %v; nothing was deleted", plan.TaskID, err)
		}
		res.Stopped = true
	}
	if plan.RemoveWorktree {
		if err := c.git.RemoveWorktree(ctx, plan.RepoRoot, plan.Worktree); err != nil {
			return res, userErr(ErrDeleteFailed, err, "removing worktree %s failed: %v; the task is kept (fix it, then delete again)", plan.Worktree, err)
		}
		res.WorktreeRemoved = true
	} else if plan.WorktreeMissing {
		if err := c.git.PruneWorktrees(ctx, plan.RepoRoot); err != nil {
			return res, userErr(ErrDeleteFailed, err, "git worktree prune failed: %v; the task is kept (fix it, then delete again)", err)
		}
	}
	if plan.DeleteBranch {
		if err := c.git.DeleteBranch(ctx, plan.RepoRoot, plan.Branch); err != nil {
			return res, userErr(ErrDeleteFailed, err, "deleting branch %s failed: %v; the task is kept (fix it, then delete again)", plan.Branch, err)
		}
		res.BranchDeleted = true
	}
	// Before the records go: a context file left behind would show up in a
	// new task of the same name.
	archived, err := taskctx.Archive(c.paths.Home, plan.TaskID, c.now())
	if err != nil {
		return res, userErr(ErrDeleteFailed, err, "archiving the context file of %q failed: %v; the task is kept (fix it, then delete again)", plan.TaskID, err)
	}
	res.ContextArchived = archived
	if _, err := c.store.Tasks().Delete(ctx, store.DeleteInput{TaskID: plan.TaskID, Payload: map[string]any{
		"branch": plan.Branch, "worktree": plan.Worktree, "stopped": res.Stopped,
		"worktree_removed": res.WorktreeRemoved, "branch_deleted": res.BranchDeleted,
	}}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return res, userErr(ErrTaskNotFound, err, "no task named %q", plan.TaskID)
		}
		return res, err
	}
	return res, nil
}

func (c *Controller) planDelete(ctx context.Context, name string) (DeletePlan, store.Session, error) {
	task, err := c.store.Tasks().Get(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return DeletePlan{}, store.Session{}, userErr(ErrTaskNotFound, err, "no task named %q (`agentctl ls --closed` lists every task)", name)
	}
	if err != nil {
		return DeletePlan{}, store.Session{}, err
	}
	repo, err := c.store.Repos().Get(ctx, task.RepoID)
	if err != nil {
		return DeletePlan{}, store.Session{}, err
	}
	plan := DeletePlan{
		TaskID: task.ID, RepoRoot: repo.RootPath, Worktree: task.WorktreePath, Branch: task.Branch,
		BaseRef: task.BaseRef, Closed: task.State == domain.TaskClosed, UnmergedCommits: -1,
	}
	if taskctx.HasNotes(c.paths.Home, task.ID) {
		plan.ContextFile, plan.ContextArchiveDir = taskctx.Path(c.paths.Home, task.ID), c.paths.ContextArchive
	}
	sess, err := c.store.Sessions().LatestForTask(ctx, task.ID)
	hasSession := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return plan, store.Session{}, err
	}
	plan.Owned = hasSession && sess.Origin == domain.OriginLaunched
	if hasSession {
		plan.SessionName, plan.Identity = sess.SessionName, sess.Identity
		if err := c.checkDeletable(ctx, &plan, sess); err != nil {
			return plan, sess, err
		}
	}
	if !plan.Owned {
		plan.Kept = append(plan.Kept,
			"keeps worktree "+DisplayPath(plan.Worktree)+" (agentctl did not create it)",
			"keeps branch "+DisplayPath(plan.Branch)+" (agentctl did not create it)")
		return plan, sess, nil
	}
	if err := c.planWorktreeRemoval(ctx, &plan); err != nil {
		return plan, sess, err
	}
	if err := c.planBranchDeletion(ctx, &plan); err != nil {
		return plan, sess, err
	}
	return plan, sess, nil
}

// checkDeletable refuses while an agent still runs that agentctl may not
// stop, and sets plan.Stop when it may.
func (c *Controller) checkDeletable(ctx context.Context, plan *DeletePlan, sess store.Session) error {
	id := plan.TaskID
	l, _ := c.liveSession(ctx, sess)
	switch {
	case l.Status == domain.LivenessUnverified:
		return userErr(ErrDeleteRefused, nil, "refusing to delete %q: cannot check whether its agent still runs (%s); nothing was deleted, try again", id, l)
	case !l.IsAlive():
		return nil
	case !plan.Owned:
		return userErr(ErrDeleteRefused, nil, "refusing to delete %q: its agent still runs in pane %s and agentctl did not start it; exit the agent, then delete again", id, sess.Identity.PaneID)
	}
	plan.Stop = true
	return nil
}

func (c *Controller) planWorktreeRemoval(ctx context.Context, plan *DeletePlan) error {
	shown := DisplayPath(plan.Worktree)
	if !filepath.IsAbs(plan.Worktree) || samePath(plan.Worktree, plan.RepoRoot) {
		plan.Kept = append(plan.Kept, "keeps "+shown+" (it is the main checkout)")
		return nil
	}
	listed := false
	if wts, err := c.git.ListWorktrees(ctx, plan.RepoRoot); err == nil {
		for _, wt := range wts {
			if samePath(wt.Path, plan.Worktree) {
				listed = true
				break
			}
		}
	} else {
		return userErr(ErrDeleteFailed, err, "cannot list the worktrees of %s: %v; nothing was deleted", plan.RepoRoot, err)
	}
	_, statErr := os.Lstat(plan.Worktree)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		plan.WorktreeMissing = true
	case !listed:
		plan.Kept = append(plan.Kept, "keeps "+shown+" (git does not list it as a worktree of "+DisplayPath(plan.RepoRoot)+")")
	default:
		plan.RemoveWorktree = true
		if st, err := c.git.Status(ctx, plan.Worktree); err == nil {
			plan.DirtyFiles = len(st.Entries)
		}
	}
	return nil
}

func (c *Controller) planBranchDeletion(ctx context.Context, plan *DeletePlan) error {
	if plan.Branch == "" || plan.Branch == "HEAD" || plan.Branch == plan.BaseRef {
		plan.Kept = append(plan.Kept, "keeps branch "+DisplayPath(plan.Branch)+" (it is not the task's own branch)")
		return nil
	}
	ok, err := c.git.BranchExists(ctx, plan.RepoRoot, plan.Branch)
	if err != nil {
		return userErr(ErrDeleteFailed, err, "cannot check branch %s: %v; nothing was deleted", plan.Branch, err)
	}
	if !ok {
		return nil
	}
	plan.DeleteBranch = true
	if plan.BaseRef != "" && plan.BaseRef != "HEAD" {
		if n, err := c.git.CountCommits(ctx, plan.RepoRoot, plan.Branch, plan.BaseRef); err == nil {
			plan.UnmergedCommits = n
		}
	}
	return nil
}

// samePath compares two paths after resolving symlinks where possible.
func samePath(a, b string) bool {
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// DeletePlanLines describes a plan for a confirmation prompt, one line per
// step, warnings included. Sanitize before display.
func DeletePlanLines(p DeletePlan) []string {
	var out []string
	if p.Stop {
		out = append(out, "kills tmux session "+DisplayPath(p.SessionName)+" (pane "+p.Identity.PaneID+"): the agent stops now")
	}
	switch {
	case p.RemoveWorktree && p.DirtyFiles > 0:
		out = append(out, "removes worktree "+DisplayPath(p.Worktree)+" · warning: "+plural(p.DirtyFiles, "uncommitted or untracked file")+" will be lost")
	case p.RemoveWorktree:
		out = append(out, "removes worktree "+DisplayPath(p.Worktree)+" (no uncommitted changes)")
	case p.WorktreeMissing:
		out = append(out, "worktree "+DisplayPath(p.Worktree)+" is already gone; prunes git's record of it")
	}
	if p.DeleteBranch {
		switch {
		case p.UnmergedCommits > 0:
			out = append(out, "deletes branch "+DisplayPath(p.Branch)+" · warning: "+plural(p.UnmergedCommits, "commit")+" not on "+DisplayPath(p.BaseRef)+" (lost unless pushed or merged)")
		case p.UnmergedCommits == 0:
			out = append(out, "deletes branch "+DisplayPath(p.Branch)+" (no commits beyond "+DisplayPath(p.BaseRef)+")")
		default:
			out = append(out, "deletes branch "+DisplayPath(p.Branch))
		}
	}
	if p.ContextFile != "" {
		out = append(out, "archives context file "+DisplayPath(p.ContextFile)+" to "+DisplayPath(p.ContextArchiveDir)+"/")
	}
	out = append(out, p.Kept...)
	return append(out, "forgets task "+p.TaskID+" and its messages; the name is free again")
}

// DeleteSummary is the one-line report of a completed delete.
func DeleteSummary(r DeleteResult) string {
	parts := []string{"deleted " + r.TaskID}
	if r.Stopped {
		parts = append(parts, "stopped tmux session "+DisplayPath(r.SessionName))
	}
	if r.WorktreeRemoved {
		parts = append(parts, "removed worktree "+DisplayPath(r.Worktree))
	}
	if r.BranchDeleted {
		parts = append(parts, "deleted branch "+DisplayPath(r.Branch))
	}
	if r.ContextArchived != "" {
		parts = append(parts, "archived context to "+DisplayPath(r.ContextArchived))
	}
	if !r.Owned {
		parts = append(parts, "worktree and branch kept (agentctl did not create them)")
	}
	return strings.Join(parts, " · ")
}
