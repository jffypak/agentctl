package controller

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/gitx"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/tmux"
)

// EnvAdoptCommands lists the pane_current_command names `agentctl adopt`
// offers as candidates (comma-separated; default "claude"). The list is only
// a convenience: `adopt %N --name <name>` accepts any live pane.
const EnvAdoptCommands = "AGENTCTL_ADOPT_COMMANDS"

// DefaultAdoptCommands is used when EnvAdoptCommands is unset or empty.
var DefaultAdoptCommands = []string{"claude"}

// Adopt error kinds (errors.Is).
var (
	ErrNoTmuxServer   = errors.New("no tmux server")
	ErrPaneNotFound   = errors.New("pane not found")
	ErrPaneRegistered = errors.New("pane already registered")
)

// AdoptCandidate is one unregistered live pane offered by `agentctl adopt`.
type AdoptCandidate struct {
	PaneID      string
	PanePID     int
	SessionID   string
	SessionName string // as tmux reports it; sanitize before display
	WindowIndex int
	Command     string // pane_current_command
	Cwd         string // pane_current_path
}

// AdoptCandidates is the candidate list for one tmux server.
type AdoptCandidates struct {
	Socket   string
	Commands []string // the names matched (nil when All)
	Panes    []AdoptCandidate
}

// CandidateQuery filters the candidate list.
type CandidateQuery struct {
	All bool // every unregistered live pane, whatever its command
}

// ListAdoptCandidates lists live panes on the resolved tmux server whose
// command is a configured agent name and whose identity (server pid, pane id,
// pane pid) is not bound to any session. It never registers anything: the
// user picks a pane explicitly (invariant 5). A missing server yields an
// empty list.
func (c *Controller) ListAdoptCandidates(ctx context.Context, q CandidateQuery) (AdoptCandidates, error) {
	if err := c.requireTmux(); err != nil {
		return AdoptCandidates{}, err
	}
	socket, err := tmux.ResolveSocket(c.Getenv, c.uid)
	if err != nil {
		return AdoptCandidates{}, userErr(ErrNoTmuxServer, err, "%v", err)
	}
	out := AdoptCandidates{Socket: socket}
	if !q.All {
		out.Commands = c.adoptCommands()
	}
	snap, err := c.snapshot(ctx, socket)
	if errors.Is(err, tmux.ErrNoServer) {
		return out, nil
	}
	if err != nil {
		return out, userErr(ErrNoTmuxServer, err, "cannot list tmux panes on %s: %v", socket, err)
	}
	registered, err := c.registeredPanes(ctx, socket, snap.ServerPID)
	if err != nil {
		return out, err
	}
	for _, p := range snap.Panes {
		if p.Dead || registered[paneKey(p.PaneID, p.PanePID)] || (!q.All && !containsString(out.Commands, p.CurrentCommand)) {
			continue
		}
		out.Panes = append(out.Panes, AdoptCandidate{
			PaneID: p.PaneID, PanePID: p.PanePID, SessionID: p.SessionID, SessionName: p.SessionName,
			WindowIndex: p.WindowIndex, Command: p.CurrentCommand, Cwd: p.CurrentPath,
		})
	}
	return out, nil
}

// AdoptRequest is `agentctl adopt %N --name <name>`.
type AdoptRequest struct {
	PaneID string
	Name   string
}

// AdoptResult describes the registered task.
type AdoptResult struct {
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
	State       domain.AgentState
	Confidence  domain.Confidence
}

// Adopt registers an existing live pane as task req.Name: the pane's cwd
// gives the worktree (its git toplevel), branch (its HEAD) and repo (the main
// repo via --git-common-dir); the base is merge-base(HEAD, main checkout
// HEAD), falling back to HEAD. The identity (socket, server pid, session id,
// pane id, pane pid) is captured now. The session starts unknown / none
// (no hooks until the user merges `agentctl hooks-snippet`) with origin
// adopted; task.created and session.adopted are appended atomically with the
// rows. Every listed failure is a one-line *Error and writes nothing.
func (c *Controller) Adopt(ctx context.Context, req AdoptRequest) (AdoptResult, error) {
	if err := domain.ValidateSlug(req.Name); err != nil {
		return AdoptResult{}, userErr(ErrInvalidName, err, "%v", err)
	}
	if !isPaneID(req.PaneID) {
		return AdoptResult{}, userErr(ErrPaneNotFound, nil, "%q is not a tmux pane id like %%3 (run `agentctl adopt` to list candidates)", req.PaneID)
	}
	if err := c.requireTmux(); err != nil {
		return AdoptResult{}, err
	}
	if _, err := c.lookPath("git"); err != nil {
		return AdoptResult{}, userErr(ErrGitMissing, err, "git not found on PATH (install git 2.40 or newer)")
	}
	if exists, err := c.store.Tasks().Exists(ctx, req.Name); err != nil {
		return AdoptResult{}, err
	} else if exists {
		return AdoptResult{}, taskExists(req.Name)
	}
	pane, id, err := c.adoptablePane(ctx, req.PaneID)
	if err != nil {
		return AdoptResult{}, err
	}
	loc, err := c.paneLocation(ctx, pane)
	if err != nil {
		return AdoptResult{}, err
	}
	repoRow, err := c.store.Repos().Upsert(ctx, loc.repo.Root, loc.repo.Name)
	if err != nil {
		return AdoptResult{}, err
	}
	task := store.Task{
		ID: req.Name, RepoID: repoRow.ID, Name: req.Name, WorktreePath: loc.worktree,
		Branch: loc.head.Ref, BaseRef: loc.baseRef, BaseSHA: loc.baseSHA,
	}
	_, sess, err := c.store.Sessions().RegisterAdopted(ctx, task, store.NewSession{
		TaskID: req.Name, AgentType: c.agent.Name(), Origin: domain.OriginAdopted, Identity: id,
		SessionName: pane.SessionName, State: domain.StateUnknown, Confidence: domain.ConfidenceNone,
		Detail: "adopted; no hooks yet",
		Events: []store.EventInput{
			{Type: domain.EventTaskCreated, Payload: map[string]any{
				"repo": loc.repo.Name, "repo_root": loc.repo.Root, "branch": loc.head.Ref, "worktree": loc.worktree,
				"base_ref": loc.baseRef, "base_sha": loc.baseSHA, "origin": string(domain.OriginAdopted),
			}},
			{Type: domain.EventSessionAdopted, Payload: map[string]any{
				"socket": id.Socket, "server_pid": id.ServerPID, "session_id": id.SessionID,
				"session_name": domain.SanitizeTerminalText(pane.SessionName), "pane_id": id.PaneID, "pane_pid": id.PanePID,
				"agent_type": c.agent.Name(), "command": domain.SanitizeTerminalText(pane.CurrentCommand),
			}},
		},
	})
	switch {
	case errors.Is(err, store.ErrTaskExists):
		return AdoptResult{}, taskExists(req.Name)
	case errors.Is(err, store.ErrPaneRegistered):
		return AdoptResult{}, userErr(ErrPaneRegistered, err, "pane %s is already registered (`agentctl ls --closed` shows its task)", req.PaneID)
	case err != nil:
		return AdoptResult{}, err
	}
	return AdoptResult{
		TaskID: req.Name, Repo: loc.repo.Name, RepoRoot: loc.repo.Root, Branch: loc.head.Ref, Worktree: loc.worktree,
		BaseRef: loc.baseRef, BaseSHA: loc.baseSHA, SessionID: sess.ID, SessionName: sess.SessionName,
		Identity: sess.Identity, State: sess.State, Confidence: sess.Confidence,
	}, nil
}

// adoptablePane finds a live, unregistered pane on the resolved server and
// returns it with the identity captured from the same snapshot.
func (c *Controller) adoptablePane(ctx context.Context, paneID string) (tmux.Pane, domain.Identity, error) {
	socket, err := tmux.ResolveSocket(c.Getenv, c.uid)
	if err != nil {
		return tmux.Pane{}, domain.Identity{}, userErr(ErrNoTmuxServer, err, "%v", err)
	}
	snap, err := c.snapshot(ctx, socket)
	if errors.Is(err, tmux.ErrNoServer) {
		return tmux.Pane{}, domain.Identity{}, userErr(ErrNoTmuxServer, err, "no tmux server is running on %s (start tmux, or set %s)", socket, tmux.EnvSocket)
	}
	if err != nil {
		return tmux.Pane{}, domain.Identity{}, userErr(ErrNoTmuxServer, err, "cannot list tmux panes on %s: %v", socket, err)
	}
	pane, ok := snap.Find(paneID)
	if !ok {
		return tmux.Pane{}, domain.Identity{}, userErr(ErrPaneNotFound, nil, "pane %s not found on tmux server %s (run `agentctl adopt` to list candidates)", paneID, socket)
	}
	if pane.Dead {
		return tmux.Pane{}, domain.Identity{}, userErr(ErrPaneNotFound, nil, "pane %s has exited; only a running agent pane can be adopted", paneID)
	}
	id := domain.Identity{Socket: socket, ServerPID: snap.ServerPID, SessionID: pane.SessionID, PaneID: pane.PaneID, PanePID: pane.PanePID}
	if owner, err := c.store.Sessions().PaneOwner(ctx, id); err == nil {
		return tmux.Pane{}, domain.Identity{}, userErr(ErrPaneRegistered, nil, "pane %s is already registered as task %q (`agentctl ls --closed` lists every task)", paneID, owner.TaskID)
	} else if !errors.Is(err, store.ErrNotFound) {
		return tmux.Pane{}, domain.Identity{}, err
	}
	return pane, id, nil
}

// paneLoc is where an adopted pane works.
type paneLoc struct {
	repo     gitx.RepoInfo
	worktree string
	head     gitx.Head
	baseRef  string
	baseSHA  string
}

func (c *Controller) paneLocation(ctx context.Context, pane tmux.Pane) (paneLoc, error) {
	cwd := pane.CurrentPath
	shown := domain.SanitizeTerminalText(cwd)
	if !filepath.IsAbs(cwd) {
		return paneLoc{}, userErr(ErrNotARepo, nil, "tmux reports no working directory for pane %s (cd that pane into the task's worktree, then retry)", pane.PaneID)
	}
	top, err := c.git.Toplevel(ctx, cwd)
	if err != nil {
		return paneLoc{}, userErr(ErrNotARepo, err, "pane %s's working directory %s is not inside a git repository (cd that pane into the task's worktree, then retry)", pane.PaneID, shown)
	}
	repo, err := c.git.MainRepo(ctx, cwd)
	if err != nil {
		return paneLoc{}, userErr(ErrNotARepo, err, "cannot resolve the git repository for %s: %v", shown, err)
	}
	head, err := c.git.Head(ctx, top)
	if err != nil {
		if errors.Is(err, gitx.ErrNoCommits) {
			return paneLoc{}, userErr(ErrNoCommits, err, "%s has no commits yet (make an initial commit first)", domain.SanitizeTerminalText(top))
		}
		return paneLoc{}, err
	}
	loc := paneLoc{repo: repo, worktree: top, head: head, baseRef: "HEAD", baseSHA: head.SHA}
	if main, err := c.git.Head(ctx, repo.Root); err == nil {
		if mb, err := c.git.MergeBase(ctx, top, head.SHA, main.SHA); err == nil && mb != "" {
			loc.baseRef, loc.baseSHA = main.Ref, mb
		}
	}
	return loc, nil
}

// registeredPanes indexes the pane identities on a socket that are bound to a
// session, for the given (live) server pid.
func (c *Controller) registeredPanes(ctx context.Context, socket string, serverPID int) (map[string]bool, error) {
	sessions, err := c.store.Sessions().OnSocket(ctx, socket)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, s := range sessions {
		if s.Identity.ServerPID == serverPID {
			out[paneKey(s.Identity.PaneID, s.Identity.PanePID)] = true
		}
	}
	return out, nil
}

func paneKey(paneID string, panePID int) string { return paneID + "/" + strconv.Itoa(panePID) }

// adoptCommands parses EnvAdoptCommands.
func (c *Controller) adoptCommands() []string {
	var names []string
	for _, n := range strings.Split(c.Getenv(EnvAdoptCommands), ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return append([]string(nil), DefaultAdoptCommands...)
	}
	return names
}

func (c *Controller) requireTmux() error {
	if c.tmuxInjected {
		return nil
	}
	if _, err := c.lookPath("tmux"); err != nil {
		return userErr(ErrTmuxMissing, err, "tmux not found on PATH (install tmux 3.3 or newer)")
	}
	return nil
}

func isPaneID(s string) bool {
	if len(s) < 2 || s[0] != '%' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
