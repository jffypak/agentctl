package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/jffypak/agentctl/internal/controller"
)

const adoptUsage = "usage: agentctl adopt [--all]  |  agentctl adopt %<pane> --name <name>"

// cmdAdopt: `agentctl adopt [--all]` lists unregistered candidate panes;
// `agentctl adopt %N --name <name>` registers one (origin adopted, state
// unknown/none, events task.created + session.adopted). Nothing is ever
// adopted implicitly: the user names the pane.
func cmdAdopt(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "adopt")
	name := fs.String("name", "", "task name for the adopted pane")
	all := fs.Bool("all", false, "list every unregistered pane, whatever its command")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	listing := len(pos) == 0 && *name == ""
	adopting := len(pos) == 1 && *name != "" && !*all
	if !listing && !adopting {
		return sys.usageError("adopt", "%s", adoptUsage)
	}
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()
	if listing {
		return adoptList(ctx, sys, ctl, *all)
	}
	res, err := ctl.Adopt(ctx, controller.AdoptRequest{PaneID: pos[0], Name: *name})
	if err != nil {
		return sys.fail(err)
	}
	fmt.Fprintf(sys.stdout, "adopted %s as %s · %s · %s · state %s (no hooks yet: merge `agentctl hooks-snippet` into your Claude settings for live state)\n",
		res.Identity.PaneID, res.TaskID, clean(res.Branch), clean(res.Worktree), res.State)
	if res.Warning != "" {
		fmt.Fprintf(sys.stderr, "agentctl: warning: %s\n", controller.OneLine(res.Warning))
	}
	return exitOK
}

func adoptList(ctx context.Context, sys *system, ctl *controller.Controller, all bool) int {
	list, err := ctl.ListAdoptCandidates(ctx, controller.CandidateQuery{All: all})
	if err != nil {
		return sys.fail(err)
	}
	if list.Warning != "" {
		fmt.Fprintf(sys.stderr, "agentctl: warning: %s\n", controller.OneLine(list.Warning))
	}
	for _, h := range list.Hints {
		fmt.Fprintf(sys.stderr, "agentctl: note: %s\n", controller.OneLine(h))
	}
	what := "unregistered panes"
	if !all {
		what = "unregistered panes running " + strings.Join(list.Commands, ", ")
	}
	if len(list.Panes) == 0 {
		fmt.Fprintf(sys.stdout, "No %s on tmux server %s (`agentctl adopt --all` lists every pane; %s sets the command names).\n",
			what, clean(list.Socket), controller.EnvAdoptCommands)
		return exitOK
	}
	fmt.Fprintf(sys.stdout, "%s on tmux server %s:\n\n", capitalize(what), clean(list.Socket))
	tw := tabwriter.NewWriter(sys.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PANE\tSESSION:WINDOW\tCOMMAND\tCWD")
	for _, p := range list.Panes {
		fmt.Fprintf(tw, "%s\t%s:%s\t%s\t%s\n", p.PaneID, clean(p.SessionName), strconv.Itoa(p.WindowIndex), clean(p.Command), clean(p.Cwd))
	}
	_ = tw.Flush()
	fmt.Fprintf(sys.stdout, "\nadopt one with: agentctl adopt %s --name <name>\n", list.Panes[0].PaneID)
	return exitOK
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
