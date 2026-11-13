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

// seedText gives task id's session the hook text the details pane shows;
// unread stamps a turn end after the (never) last view.
func (f *fixture) seedText(t *testing.T, id, prompt, message, model string, unread bool) {
	t.Helper()
	f.exec(t, `UPDATE sessions SET last_prompt = ?, last_message = ?, model = ? WHERE task_id = ?`, prompt, message, model, id)
	if unread {
		f.exec(t, `UPDATE sessions SET turn_ended_at = ? WHERE task_id = ?`, fixedNow.Add(-time.Minute).UnixMilli(), id)
	}
}

// seedCommandCenter is everyState plus the text and unread marks of the
// command-center screens.
func (f *fixture) seedCommandCenter(t *testing.T) {
	t.Helper()
	f.seed(t, everyState)
	f.seedText(t, "legacy-pane", "", "", "", false)
	f.seedText(t, "docs-site", "Fix the broken links in docs/guide.md and docs/api.md",
		"## Fixed\n\nI fixed **14 broken links** in `docs/guide.md` and `docs/api.md`.\n\n- 9 pointed at the old `/v1/` paths\n- 5 had a typo in the anchor\n\nI ran the link checker again and it reports no errors.",
		"claude-opus-5", true)
	f.seedText(t, "api-docs", "Regenerate the API reference", "Regenerated the reference for 42 endpoints. Two endpoints have no description yet: POST /orders/refund and GET /orders/{id}/events.", "claude-opus-5", true)
}
