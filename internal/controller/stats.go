package controller

import (
	"context"
	"slices"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

// Stats is the success-metric report behind `agentctl stats`
// (ARCHITECTURE.md §Success metric).
type Stats struct {
	// Runs is the number of TUI runs (ui.opened events with a ui_run_id).
	Runs int
	// RunsWithAction / RunsWithoutAction split Runs by whether the run
	// recorded at least one ui.action.
	RunsWithAction    int
	RunsWithoutAction int
	// MedianOpenToFirstAction is the median time from ui.opened to the run's
	// first ui.action over the RunsWithAction runs (0 when there are none).
	MedianOpenToFirstAction time.Duration
	// Attaches counts ui.action{attach}; Prompts counts message.queued, i.e.
	// every prompt sent through agentctl (TUI composer or `agentctl send`).
	Attaches int
	Prompts  int
}

// AttachesPerPrompt is Attaches / Prompts; ok is false when no prompt was sent.
func (s Stats) AttachesPerPrompt() (ratio float64, ok bool) {
	if s.Prompts == 0 {
		return 0, false
	}
	return float64(s.Attaches) / float64(s.Prompts), true
}

// Stats reads the instrumentation events and computes the report.
func (c *Controller) Stats(ctx context.Context) (Stats, error) {
	ev := c.store.Events()
	runs, err := ev.UIRuns(ctx)
	if err != nil {
		return Stats{}, err
	}
	attaches, err := ev.CountUIActions(ctx, UIActionAttach)
	if err != nil {
		return Stats{}, err
	}
	prompts, err := ev.CountType(ctx, domain.EventMessageQueued)
	if err != nil {
		return Stats{}, err
	}
	return computeStats(runs, attaches, prompts), nil
}

// computeStats is the pure part of Stats.
func computeStats(runs []store.UIRunStat, attaches, prompts int) Stats {
	s := Stats{Runs: len(runs), Attaches: attaches, Prompts: prompts}
	var waits []time.Duration
	for _, r := range runs {
		if !r.HasAction {
			s.RunsWithoutAction++
			continue
		}
		s.RunsWithAction++
		waits = append(waits, max(r.FirstActionAt.Sub(r.OpenedAt), 0))
	}
	s.MedianOpenToFirstAction = median(waits)
	return s
}

// median of ds (mean of the two middle values for an even count; 0 if empty).
func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := slices.Clone(ds)
	slices.Sort(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
