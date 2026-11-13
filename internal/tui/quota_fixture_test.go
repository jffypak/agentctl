package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/quota"
)

// seedQuota saves a Claude snapshot (fresh, plenty left) and writes a Codex
// rollout (12 minutes old, 5-hour window nearly used up) into the fixture's
// homes.
func (f *fixture) seedQuota(t *testing.T) {
	t.Helper()
	p, err := paths.Resolve(f.env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	err = quota.SaveClaude(p.Home, quota.Snapshot{
		Agent:      quota.AgentClaude,
		FiveHour:   &quota.Window{UsedPercent: 42, ResetsAt: fixedNow.Add(2*time.Hour + 10*time.Minute)},
		Weekly:     &quota.Window{UsedPercent: 18, ResetsAt: fixedNow.Add(76 * time.Hour)},
		CapturedAt: fixedNow.Add(-30 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	day := filepath.Join(f.env.Getenv("HOME"), ".codex", "sessions", "2026", "11", "07")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","rate_limits":{"limit_id":"codex","primary":{"used_percent":85,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":16,"window_minutes":10080,"resets_at":%d}}}}`+"\n",
		fixedNow.Add(-12*time.Minute).Format(time.RFC3339), fixedNow.Add(45*time.Minute).Unix(), fixedNow.Add(5*24*time.Hour).Unix())
	if err := os.WriteFile(filepath.Join(day, "rollout-2026-11-07T09-00-00-x.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}
