package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jffypak/agentctl/internal/controller"
)

// statsSchemaVersion is bumped on any incompatible change to `stats --json`.
const statsSchemaVersion = 1

// cmdStats: `agentctl stats [--json]` prints the success metric
// (ARCHITECTURE.md §Success metric): the median time from opening the TUI to
// its first action and over how many runs, the runs that ended with no
// action, and attaches per prompt sent.
func cmdStats(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "stats")
	asJSON := fs.Bool("json", false, "print a stable JSON document")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	if len(pos) != 0 {
		return sys.usageError("stats", "usage: agentctl stats [--json]")
	}
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()
	s, err := ctl.Stats(ctx)
	if err != nil {
		return sys.fail(err)
	}
	if *asJSON {
		enc := json.NewEncoder(sys.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(statsDocument(s)); err != nil {
			return sys.fail(err)
		}
		return exitOK
	}
	printStats(sys.stdout, s)
	return exitOK
}

// statsJSON is the stable --json schema. Nulls mean "no data yet".
type statsJSON struct {
	SchemaVersion              int      `json:"schema_version"`
	Runs                       int      `json:"runs"`
	RunsWithAction             int      `json:"runs_with_action"`
	RunsWithoutAction          int      `json:"runs_without_action"`
	MedianOpenToFirstActionMS  *int64   `json:"median_open_to_first_action_ms"`
	MedianOpenToFirstActionSec *float64 `json:"median_open_to_first_action_seconds"`
	Attaches                   int      `json:"attaches"`
	Prompts                    int      `json:"prompts"`
	AttachesPerPrompt          *float64 `json:"attaches_per_prompt"`
}

func statsDocument(s controller.Stats) statsJSON {
	doc := statsJSON{
		SchemaVersion: statsSchemaVersion, Runs: s.Runs, RunsWithAction: s.RunsWithAction,
		RunsWithoutAction: s.RunsWithoutAction, Attaches: s.Attaches, Prompts: s.Prompts,
	}
	if s.RunsWithAction > 0 {
		ms := s.MedianOpenToFirstAction.Milliseconds()
		sec := s.MedianOpenToFirstAction.Seconds()
		doc.MedianOpenToFirstActionMS, doc.MedianOpenToFirstActionSec = &ms, &sec
	}
	if r, ok := s.AttachesPerPrompt(); ok {
		doc.AttachesPerPrompt = &r
	}
	return doc
}

func printStats(w io.Writer, s controller.Stats) {
	median := "—"
	if s.RunsWithAction > 0 {
		median = formatDuration(s.MedianOpenToFirstAction)
	}
	ratio := "—"
	if r, ok := s.AttachesPerPrompt(); ok {
		ratio = strconv.FormatFloat(r, 'f', 2, 64)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "TUI runs\t%d\n", s.Runs)
	fmt.Fprintf(tw, "open → first action (median)\t%s\t(n=%d, target < 60s)\n", median, s.RunsWithAction)
	fmt.Fprintf(tw, "runs with no action\t%d\n", s.RunsWithoutAction)
	fmt.Fprintf(tw, "attaches per prompt\t%s\t(%d attaches / %d prompts)\n", ratio, s.Attaches, s.Prompts)
	_ = tw.Flush()
}

// formatDuration: 14.5s under a minute, else 2m5s (whole seconds).
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return strings.TrimSuffix(strconv.FormatFloat(d.Round(100*time.Millisecond).Seconds(), 'f', 1, 64), ".0") + "s"
	}
	return d.Round(time.Second).String()
}
