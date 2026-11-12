package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// OverviewQuery scopes Overview.
type OverviewQuery struct {
	Cwd           string // scope to the repo containing Cwd; outside a repo (or "") -> all repos
	AllRepos      bool   // ignore Cwd
	IncludeClosed bool
}

// SectionCounts counts rows per attention section.
type SectionCounts struct {
	NeedsYou int `json:"needs_you"`
	Idle     int `json:"idle"`
	Running  int `json:"running"`
	Initial  int `json:"initial"`
	Unknown  int `json:"unknown"`
	Closed   int `json:"closed"`
}

// Overview is the attention-sorted read model behind `ls` and the TUI list.
type Overview struct {
	AllRepos bool   // true outside a repo
	Repo     string // repo name when scoped
	RepoRoot string
	Rows     []TaskView // section order, then state_since oldest first
	Counts   SectionCounts
	Now      time.Time
}

// TaskView is one task as the presentation layers see it (no store types).
type TaskView struct {
	ID        string
	Name      string
	Goal      string
	Repo      string
	RepoRoot  string
	TaskState domain.TaskState
	Branch    string
	Worktree  string
	BaseRef   string
	BaseSHA   string
	CreatedAt time.Time

	HasSession     bool
	SessionID      int64
	AgentType      string
	Origin         domain.Origin
	Identity       domain.Identity
	SessionName    string
	AgentSessionID string
	HasHook        bool // at least one hook event was matched to the session
	AgentState     domain.AgentState
	Confidence     domain.Confidence
	StateSince     time.Time // task creation time while no session exists
	LastDetail     string
	EndedAt        time.Time

	Messages  domain.MessageSummary
	Attention domain.Attention
	Age       time.Duration // Now - StateSince
}

// Header renders `agents / <repo> · N running · M need you`.
func (o Overview) Header() string {
	scope := o.Repo
	if o.AllRepos || scope == "" {
		scope = "all repos"
	}
	return fmt.Sprintf("agents / %s · %d running · %d need you", scope, o.Counts.Running, o.Counts.NeedsYou)
}

// Overview reads every task in scope in one consistent query and classifies
// it. It does not probe tmux; call Reconcile first for fresh liveness.
func (c *Controller) Overview(ctx context.Context, q OverviewQuery) (Overview, error) {
	out := Overview{AllRepos: true, Now: c.now()}
	filter := store.OverviewFilter{IncludeClosed: q.IncludeClosed}
	if !q.AllRepos && q.Cwd != "" {
		if info, err := c.git.MainRepo(ctx, q.Cwd); err == nil {
			out.AllRepos, out.Repo, out.RepoRoot = false, info.Name, info.Root
			repo, err := c.store.Repos().ByPath(ctx, info.Root)
			if err != nil {
				return out, nil // a repo agentctl has never launched in: nothing to list
			}
			filter.RepoID = repo.ID
		}
	}
	rows, err := c.store.Overview(ctx, filter)
	if err != nil {
		return Overview{}, err
	}
	for _, r := range rows {
		v := taskView(r, out.Now)
		switch v.Attention.Section {
		case domain.SectionNeedsYou:
			out.Counts.NeedsYou++
		case domain.SectionIdle:
			out.Counts.Idle++
		case domain.SectionRunning:
			out.Counts.Running++
		case domain.SectionInitial:
			out.Counts.Initial++
		case domain.SectionUnknown:
			out.Counts.Unknown++
		case domain.SectionClosed:
			out.Counts.Closed++
		}
		out.Rows = append(out.Rows, v)
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Attention.Section != b.Attention.Section {
			return a.Attention.Section.Order() < b.Attention.Section.Order()
		}
		if !a.StateSince.Equal(b.StateSince) {
			return a.StateSince.Before(b.StateSince)
		}
		return a.ID < b.ID
	})
	return out, nil
}

func taskView(r store.OverviewRow, now time.Time) TaskView {
	v := TaskView{
		ID: r.Task.ID, Name: r.Task.Name, Goal: r.Task.Goal, Repo: r.Repo.Name, RepoRoot: r.Repo.RootPath,
		TaskState: r.Task.State, Branch: r.Task.Branch, Worktree: r.Task.WorktreePath,
		BaseRef: r.Task.BaseRef, BaseSHA: r.Task.BaseSHA, CreatedAt: r.Task.CreatedAt,
		StateSince: r.Task.CreatedAt, Messages: r.Messages,
	}
	if s := r.Session; s != nil {
		v.HasSession, v.SessionID, v.AgentType, v.Origin = true, s.ID, s.AgentType, s.Origin
		v.Identity, v.SessionName, v.AgentSessionID = s.Identity, s.SessionName, s.AgentSessionID
		v.AgentState, v.Confidence, v.StateSince, v.LastDetail, v.EndedAt = s.State, s.Confidence, s.StateSince, s.LastDetail, s.EndedAt
		v.HasHook = s.HasHook()
	}
	v.Attention = domain.ClassifyAttention(domain.AttentionInput{
		TaskState: v.TaskState, HasSession: v.HasSession, AgentState: v.AgentState,
		Confidence: v.Confidence, Messages: v.Messages, Origin: v.Origin, HasHook: v.HasHook,
	})
	if v.Age = now.Sub(v.StateSince); v.Age < 0 {
		v.Age = 0
	}
	return v
}

// EventsSince returns up to limit events after afterID (TUI polling; limit <= 0
// means store.DefaultEventsLimit).
func (c *Controller) EventsSince(ctx context.Context, afterID int64, limit int) ([]store.Event, error) {
	return c.store.Events().Since(ctx, afterID, limit)
}

// LastEventID is where a new poller starts.
func (c *Controller) LastEventID(ctx context.Context) (int64, error) {
	return c.store.Events().LastID(ctx)
}
