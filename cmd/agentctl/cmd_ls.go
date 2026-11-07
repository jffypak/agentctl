package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// lsSchemaVersion is bumped on any incompatible change to the --json output
// (documented in docs/notes/m1-walking-skeleton.md).
const lsSchemaVersion = 1

// cmdLs: agentctl ls [--json] [--closed]
func cmdLs(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "ls")
	asJSON := fs.Bool("json", false, "print the stable JSON schema")
	closed := fs.Bool("closed", false, "include closed tasks")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 0 {
		return sys.usageError("ls", "usage: agentctl ls [--json] [--closed]")
	}
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()
	if err := ctl.Reconcile(ctx); err != nil {
		fmt.Fprintf(sys.stderr, "agentctl: warning: reconcile: %s\n", controller.OneLine(err.Error()))
	}
	ov, err := ctl.Overview(ctx, controller.OverviewQuery{Cwd: sys.cwd, IncludeClosed: *closed})
	if err != nil {
		return sys.fail(err)
	}
	if *asJSON {
		enc := json.NewEncoder(sys.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(lsDocument(ov)); err != nil {
			return sys.fail(err)
		}
		return exitOK
	}
	printLs(sys.stdout, ov)
	return exitOK
}

func printLs(w io.Writer, ov controller.Overview) {
	fmt.Fprintln(w, ov.Header())
	if len(ov.Rows) == 0 {
		fmt.Fprintln(w, `No agents yet — start one with: agentctl new <name> --goal "<goal>"`)
		return
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSECTION\tSTATE\tCONFIDENCE\tAGE\tBRANCH\tWORKTREE")
	for _, r := range ov.Rows {
		state, conf := string(r.AgentState), string(r.Confidence)
		if !r.HasSession {
			state, conf = "-", "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			clean(r.Name), r.Attention.Section.Title(), state, conf, formatAge(r.Age), clean(r.Branch), clean(r.Worktree))
	}
	_ = tw.Flush()
}

// clean strips terminal control sequences from stored text before printing.
func clean(s string) string { return domain.SanitizeTerminalText(s) }

// formatAge renders a compact age: 12s, 4m, 3h, 2d.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
}

// --- JSON schema (stable; see docs/notes/m1-walking-skeleton.md) -------------

type lsJSON struct {
	SchemaVersion int                      `json:"schema_version"`
	GeneratedAt   string                   `json:"generated_at"`
	Scope         lsScopeJSON              `json:"scope"`
	Header        string                   `json:"header"`
	Counts        controller.SectionCounts `json:"counts"`
	Tasks         []lsTaskJSON             `json:"tasks"`
}

type lsScopeJSON struct {
	AllRepos bool   `json:"all_repos"`
	Repo     string `json:"repo"`
	RepoRoot string `json:"repo_root"`
}

type lsTaskJSON struct {
	ID         string                `json:"id"`
	Name       string                `json:"name"`
	Goal       string                `json:"goal"`
	Repo       string                `json:"repo"`
	RepoRoot   string                `json:"repo_root"`
	TaskState  string                `json:"task_state"`
	Section    string                `json:"section"`
	Label      string                `json:"label"`
	AgentState string                `json:"agent_state"`
	Confidence string                `json:"confidence"`
	StateSince string                `json:"state_since"`
	AgeSeconds int64                 `json:"age_seconds"`
	Branch     string                `json:"branch"`
	Worktree   string                `json:"worktree"`
	BaseRef    string                `json:"base_ref"`
	BaseSHA    string                `json:"base_sha"`
	LastDetail string                `json:"last_detail"`
	CreatedAt  string                `json:"created_at"`
	Session    *lsSessionJSON        `json:"session"`
	Messages   domain.MessageSummary `json:"messages"`
}

type lsSessionJSON struct {
	ID              int64   `json:"id"`
	AgentType       string  `json:"agent_type"`
	Origin          string  `json:"origin"`
	TmuxSocket      string  `json:"tmux_socket"`
	TmuxServerPID   int     `json:"tmux_server_pid"`
	TmuxSessionID   string  `json:"tmux_session_id"`
	TmuxSessionName string  `json:"tmux_session_name"`
	TmuxPaneID      string  `json:"tmux_pane_id"`
	PanePID         int     `json:"pane_pid"`
	AgentSessionID  string  `json:"agent_session_id"`
	EndedAt         *string `json:"ended_at"`
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func lsDocument(ov controller.Overview) lsJSON {
	doc := lsJSON{
		SchemaVersion: lsSchemaVersion,
		GeneratedAt:   rfc3339(ov.Now),
		Scope:         lsScopeJSON{AllRepos: ov.AllRepos, Repo: ov.Repo, RepoRoot: ov.RepoRoot},
		Header:        ov.Header(),
		Counts:        ov.Counts,
		Tasks:         []lsTaskJSON{},
	}
	for _, r := range ov.Rows {
		t := lsTaskJSON{
			ID: r.ID, Name: r.Name, Goal: r.Goal, Repo: r.Repo, RepoRoot: r.RepoRoot,
			TaskState: string(r.TaskState), Section: string(r.Attention.Section), Label: r.Attention.Label,
			AgentState: string(r.AgentState), Confidence: string(r.Confidence),
			StateSince: rfc3339(r.StateSince), AgeSeconds: int64(r.Age / time.Second),
			Branch: r.Branch, Worktree: r.Worktree, BaseRef: r.BaseRef, BaseSHA: r.BaseSHA,
			LastDetail: r.LastDetail, CreatedAt: rfc3339(r.CreatedAt), Messages: r.Messages,
		}
		if r.HasSession {
			s := &lsSessionJSON{
				ID: r.SessionID, AgentType: r.AgentType, Origin: string(r.Origin),
				TmuxSocket: r.Identity.Socket, TmuxServerPID: r.Identity.ServerPID, TmuxSessionID: r.Identity.SessionID,
				TmuxSessionName: r.SessionName, TmuxPaneID: r.Identity.PaneID, PanePID: r.Identity.PanePID,
				AgentSessionID: r.AgentSessionID,
			}
			if !r.EndedAt.IsZero() {
				e := rfc3339(r.EndedAt)
				s.EndedAt = &e
			}
			t.Session = s
		}
		doc.Tasks = append(doc.Tasks, t)
	}
	return doc
}
