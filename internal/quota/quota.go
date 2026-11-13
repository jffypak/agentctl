// Package quota reads how much of each agent's subscription quota is used and
// when each window resets. Neither agent has a command that prints it without
// typing into a live session, so it comes from what each agent already
// writes:
//
//   - Claude Code reports rate_limits only on a status line command's stdin.
//     `agentctl statusline` (the status line of every session agentctl
//     launches) saves them to <agentctl home>/quota/claude.json.
//   - Codex records rate_limits in every token_count event of its session
//     rollout files ($CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl).
//
// Quota is per account, not per session, so the newest snapshot of each agent
// is the answer.
package quota

import (
	"time"
)

// Window is one rate-limit window.
type Window struct {
	UsedPercent float64   // 0-100 (Claude may report a little over 100)
	ResetsAt    time.Time // zero when unknown
}

// LeftPercent is the share of the window still available, clamped to 0-100.
// A window whose reset time has passed counts as fully available.
func (w Window) LeftPercent(now time.Time) float64 {
	if w.Reset(now) {
		return 100
	}
	return min(max(100-w.UsedPercent, 0), 100)
}

// Reset reports whether the window's reset time has passed.
func (w Window) Reset(now time.Time) bool {
	return !w.ResetsAt.IsZero() && !now.Before(w.ResetsAt)
}

// Snapshot is one agent's quota as last seen.
type Snapshot struct {
	Agent      string  // "claude" or "codex"
	FiveHour   *Window // the rolling 5-hour window, nil when absent
	Weekly     *Window // the 7-day window, nil when absent
	CapturedAt time.Time
}

// Empty reports whether the snapshot has no window at all.
func (s Snapshot) Empty() bool { return s.FiveHour == nil && s.Weekly == nil }

// Agent names used in snapshots.
const (
	AgentClaude = "claude"
	AgentCodex  = "codex"
)

// Minutes of the two windows both agents report.
const (
	fiveHourMinutes = 5 * 60
	weeklyMinutes   = 7 * 24 * 60
)
