package controller

import (
	"context"
	"errors"

	"github.com/jffypak/agentctl/internal/quota"
)

// QuotaSnapshot is one agent's subscription quota as last seen (5-hour and
// weekly windows: percent used and reset time).
type QuotaSnapshot = quota.Snapshot

// QuotaWindow is one rate-limit window of a QuotaSnapshot.
type QuotaWindow = quota.Window

// Quota returns the newest quota snapshot of each agent that has one, Claude
// first. Quota is per account, so it does not depend on which tasks are
// listed. Claude's comes from <home>/quota/claude.json (saved by `agentctl
// statusline`), Codex's from its newest session rollout. An unreadable
// source is skipped and reported in err; the other snapshot is still
// returned.
func (c *Controller) Quota(_ context.Context) ([]QuotaSnapshot, error) {
	var (
		out  []QuotaSnapshot
		errs []error
	)
	if snap, ok, err := quota.ReadClaude(c.paths.Home); err != nil {
		errs = append(errs, err)
	} else if ok {
		out = append(out, snap)
	}
	if snap, ok, err := quota.ReadCodex(quota.CodexHome(c.Getenv)); err != nil {
		errs = append(errs, err)
	} else if ok {
		out = append(out, snap)
	}
	return out, errors.Join(errs...)
}
