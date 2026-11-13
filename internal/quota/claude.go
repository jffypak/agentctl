package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ClaudeFile is where `agentctl statusline` saves Claude Code's rate limits.
func ClaudeFile(home string) string { return filepath.Join(home, "quota", "claude.json") }

// claudeStatus is the part of Claude Code's status line input agentctl
// reads: rate_limits.{five_hour,seven_day}.{used_percentage,resets_at}. The
// object is present only for subscribers and only after the session's first
// API response; each window may be absent on its own.
type claudeStatus struct {
	RateLimits *struct {
		FiveHour *claudeWindow `json:"five_hour"`
		SevenDay *claudeWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type claudeWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       int64    `json:"resets_at"` // unix seconds
}

func (w *claudeWindow) window() *Window {
	if w == nil || w.UsedPercentage == nil {
		return nil
	}
	out := &Window{UsedPercent: *w.UsedPercentage}
	if w.ResetsAt > 0 {
		out.ResetsAt = time.Unix(w.ResetsAt, 0)
	}
	return out
}

// ParseClaudeStatus extracts the rate limits from one status line input. ok
// is false when the input has none (not a subscriber, or no API response
// yet), so the caller keeps the last snapshot it saved.
func ParseClaudeStatus(input []byte, now time.Time) (snap Snapshot, ok bool) {
	var st claudeStatus
	if err := json.Unmarshal(input, &st); err != nil || st.RateLimits == nil {
		return Snapshot{}, false
	}
	snap = Snapshot{
		Agent:      AgentClaude,
		FiveHour:   st.RateLimits.FiveHour.window(),
		Weekly:     st.RateLimits.SevenDay.window(),
		CapturedAt: now,
	}
	return snap, !snap.Empty()
}

// savedWindow / savedSnapshot are the on-disk form of ClaudeFile.
type savedWindow struct {
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    int64   `json:"resets_at,omitempty"`
}

type savedSnapshot struct {
	CapturedAt int64        `json:"captured_at"`
	FiveHour   *savedWindow `json:"five_hour,omitempty"`
	Weekly     *savedWindow `json:"weekly,omitempty"`
}

func toSaved(w *Window) *savedWindow {
	if w == nil {
		return nil
	}
	out := &savedWindow{UsedPercent: w.UsedPercent}
	if !w.ResetsAt.IsZero() {
		out.ResetsAt = w.ResetsAt.Unix()
	}
	return out
}

func fromSaved(w *savedWindow) *Window {
	if w == nil {
		return nil
	}
	out := &Window{UsedPercent: w.UsedPercent}
	if w.ResetsAt > 0 {
		out.ResetsAt = time.Unix(w.ResetsAt, 0)
	}
	return out
}

// SaveClaude writes snap to ClaudeFile(home) through a temp file and a
// rename (mode 0600), so a concurrent reader never sees a partial file and
// concurrent sessions simply leave the newest one.
func SaveClaude(home string, snap Snapshot) error {
	b, err := json.Marshal(savedSnapshot{
		CapturedAt: snap.CapturedAt.Unix(), FiveHour: toSaved(snap.FiveHour), Weekly: toSaved(snap.Weekly),
	})
	if err != nil {
		return err
	}
	path := ClaudeFile(home)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("quota: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".claude.json.*.tmp")
	if err != nil {
		return fmt.Errorf("quota: save: %w", err)
	}
	_, werr := tmp.Write(append(b, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("quota: save: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("quota: save: %w", err)
	}
	return nil
}

// ReadClaude loads the last saved Claude snapshot. ok is false when none was
// saved yet.
func ReadClaude(home string) (snap Snapshot, ok bool, err error) {
	b, err := os.ReadFile(ClaudeFile(home))
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("quota: read claude: %w", err)
	}
	var s savedSnapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return Snapshot{}, false, fmt.Errorf("quota: parse %s: %w", ClaudeFile(home), err)
	}
	snap = Snapshot{Agent: AgentClaude, FiveHour: fromSaved(s.FiveHour), Weekly: fromSaved(s.Weekly)}
	if s.CapturedAt > 0 {
		snap.CapturedAt = time.Unix(s.CapturedAt, 0)
	}
	return snap, !snap.Empty(), nil
}
