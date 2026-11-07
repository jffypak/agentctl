package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// showDiffFiles caps each diff section in `show` (`diff --stat` lists all).
const showDiffFiles = 10

// cmdShow: agentctl show <name> [--full]
//
// Prints the task / session / identity / state block, the diff summary
// against base_sha, and the last 40 non-empty sanitized output lines. The
// pane is captured only after its identity is verified; a disconnected or
// reused pane prints `pane unavailable (disconnected: <reason>)` instead,
// and the diff is still shown. --full writes the whole scrollback to
// logs/<task>/<UTC>.log and prints only the path.
func cmdShow(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "show")
	full := fs.Bool("full", false, "write the whole pane scrollback to logs/<task>/<UTC>.log and print its path")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 1 {
		return sys.usageError("show", "usage: agentctl show <name> [--full]")
	}
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()
	if err := ctl.Reconcile(ctx); err != nil {
		fmt.Fprintf(sys.stderr, "agentctl: warning: reconcile: %s\n", controller.OneLine(err.Error()))
	}
	if *full {
		path, err := ctl.CaptureFull(ctx, pos[0])
		if err != nil {
			return sys.fail(err)
		}
		fmt.Fprintln(sys.stdout, path)
		return exitOK
	}
	in, err := ctl.Inspect(ctx, pos[0], controller.InspectOptions{Lines: controller.DefaultInspectLines})
	if err != nil {
		return sys.fail(err)
	}
	printInspection(sys.stdout, in)
	return exitOK
}

func printInspection(w io.Writer, in controller.Inspection) {
	t := in.Task
	row := func(label, value string) { fmt.Fprintf(w, "%-10s %s\n", label, value) }
	row("task", clean(t.ID)+" · "+t.Attention.Section.Title()+" · "+clean(t.Attention.Label))
	if t.HasSession {
		row("state", fmt.Sprintf("%s · confidence %s · since %s", t.AgentState, t.Confidence, formatAge(t.Age)))
	} else {
		row("state", "no session · since "+formatAge(t.Age))
	}
	if g := oneLine(t.Goal); g != "" {
		row("goal", g)
	}
	row("repo", clean(t.Repo)+" · "+clean(t.RepoRoot))
	row("branch", clean(t.Branch)+" · base "+clean(t.BaseRef)+" @ "+shortSHA(t.BaseSHA))
	row("worktree", clean(t.Worktree))
	if t.HasSession {
		row("session", fmt.Sprintf("#%d %s · %s · tmux %s", t.SessionID, clean(t.AgentType), t.Origin, clean(t.SessionName)))
		id := t.Identity
		if id.IsZero() {
			row("identity", "none recorded")
		} else {
			row("identity", fmt.Sprintf("socket %s · server pid %d · session %s · pane %s · pane pid %d",
				clean(id.Socket), id.ServerPID, clean(id.SessionID), clean(id.PaneID), id.PanePID))
		}
	}
	row("pane", in.Pane.Note)
	if d := oneLine(t.LastDetail); d != "" {
		row("detail", d)
	}
	if m := messageCounts(t.Messages); m != "" {
		row("messages", m)
	}

	fmt.Fprintln(w)
	if in.DiffErr != "" {
		fmt.Fprintln(w, "diff unavailable: "+in.DiffErr)
	} else {
		for _, l := range in.Diff.Lines(showDiffFiles) {
			fmt.Fprintln(w, l)
		}
	}

	fmt.Fprintln(w)
	if !in.Pane.Available {
		fmt.Fprintln(w, "recent output: "+in.Pane.Note)
		return
	}
	fmt.Fprintf(w, "recent output (last %d non-empty lines)\n", len(in.Output))
	for _, l := range in.Output {
		fmt.Fprintln(w, "  "+l) // already sanitized by the controller
	}
}

// oneLine sanitizes stored text and collapses it onto one line.
func oneLine(s string) string { return controller.OneLine(clean(s)) }

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	if sha == "" {
		return "-"
	}
	return sha
}

func messageCounts(m domain.MessageSummary) string {
	var parts []string
	for _, c := range []struct {
		n    int
		name string
	}{
		{m.Queued, "queued"}, {m.Delivering, "delivering"}, {m.Sent, "sent"}, {m.Acknowledged, "acknowledged"},
		{m.Unconfirmed, "unconfirmed"}, {m.Failed, "failed"}, {m.Cancelled, "cancelled"},
	} {
		if c.n > 0 {
			parts = append(parts, strconv.Itoa(c.n)+" "+c.name)
		}
	}
	return strings.Join(parts, " · ")
}
