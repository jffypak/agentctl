package quota

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// codexTail is how much of a rollout file's end is scanned for the last
// token_count event. Codex writes one after every model response, so the
// newest one is always near the end.
const codexTail = 512 << 10

// codexFilesTried bounds how many of the newest rollout files are opened
// before giving up (a brand-new session has no token_count event yet).
const codexFilesTried = 4

// CodexHome is $CODEX_HOME, else ~/.codex ("" when neither is known).
func CodexHome(getenv func(string) string) string {
	if h := getenv("CODEX_HOME"); filepath.IsAbs(h) {
		return h
	}
	if h := getenv("HOME"); filepath.IsAbs(h) {
		return filepath.Join(h, ".codex")
	}
	return ""
}

// codexLine is the part of a rollout line agentctl reads:
//
//	{"timestamp":"…Z","type":"event_msg","payload":{"type":"token_count",
//	 "rate_limits":{"limit_id":"codex","primary":{"used_percent":5,
//	 "window_minutes":300,"resets_at":1794557822},"secondary":{…}}}}
type codexLine struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type       string `json:"type"`
		RateLimits *struct {
			LimitID   string       `json:"limit_id"`
			Primary   *codexWindow `json:"primary"`
			Secondary *codexWindow `json:"secondary"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

type codexWindow struct {
	UsedPercent   *float64 `json:"used_percent"`
	WindowMinutes int      `json:"window_minutes"`
	ResetsAt      int64    `json:"resets_at"` // unix seconds
}

// ReadCodex returns the rate limits in the newest Codex rollout that has
// any. ok is false when there is no Codex home or no rollout reports them.
func ReadCodex(codexHome string) (snap Snapshot, ok bool, err error) {
	if codexHome == "" {
		return Snapshot{}, false, nil
	}
	files, err := newestRollouts(filepath.Join(codexHome, "sessions"), codexFilesTried)
	if err != nil {
		return Snapshot{}, false, err
	}
	for _, f := range files {
		tail, err := readTail(f, codexTail)
		if err != nil {
			return Snapshot{}, false, err
		}
		if snap, ok := ParseCodexRollout(tail); ok {
			return snap, true, nil
		}
	}
	return Snapshot{}, false, nil
}

// ParseCodexRollout finds the last token_count event with rate limits in
// rollout lines (a possibly truncated first line is skipped as unparsable).
// Windows are placed by length, not by primary/secondary.
func ParseCodexRollout(b []byte) (Snapshot, bool) {
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !bytes.Contains(line, []byte(`"rate_limits"`)) {
			continue
		}
		var l codexLine
		if json.Unmarshal(line, &l) != nil || l.Payload.Type != "token_count" || l.Payload.RateLimits == nil {
			continue
		}
		rl := l.Payload.RateLimits
		if rl.LimitID != "" && rl.LimitID != "codex" {
			continue
		}
		snap := Snapshot{Agent: AgentCodex, CapturedAt: l.Timestamp}
		for _, w := range []*codexWindow{rl.Primary, rl.Secondary} {
			if w == nil || w.UsedPercent == nil {
				continue
			}
			win := &Window{UsedPercent: *w.UsedPercent}
			if w.ResetsAt > 0 {
				win.ResetsAt = time.Unix(w.ResetsAt, 0)
			}
			switch w.WindowMinutes {
			case fiveHourMinutes:
				snap.FiveHour = win
			case weeklyMinutes:
				snap.Weekly = win
			}
		}
		if !snap.Empty() {
			return snap, true
		}
	}
	return Snapshot{}, false
}

// newestRollouts returns up to n rollout files, newest first. Codex files
// them under sessions/YYYY/MM/DD, so only the newest day directories are
// listed; files in a day sort by modification time.
func newestRollouts(sessions string, n int) ([]string, error) {
	var out []string
	years, err := sortedDirs(sessions)
	if err != nil {
		return nil, err
	}
	for _, y := range years {
		months, err := sortedDirs(y)
		if err != nil {
			return nil, err
		}
		for _, mo := range months {
			days, err := sortedDirs(mo)
			if err != nil {
				return nil, err
			}
			for _, d := range days {
				files, err := rolloutsByMtime(d)
				if err != nil {
					return nil, err
				}
				out = append(out, files...)
				if len(out) >= n {
					return out[:n], nil
				}
			}
		}
	}
	return out, nil
}

// sortedDirs lists dir's subdirectories, newest name first. A missing dir is
// empty, not an error.
func sortedDirs(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("quota: list %s: %w", dir, err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	slices.Reverse(out)
	for i, name := range out {
		out[i] = filepath.Join(dir, name)
	}
	return out, nil
}

func rolloutsByMtime(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("quota: list %s: %w", dir, err)
	}
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "rollout-") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed between ReadDir and Info
		}
		files = append(files, file{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	slices.SortFunc(files, func(a, b file) int { return b.mod.Compare(a.mod) })
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.path
	}
	return out, nil
}

// readTail reads at most n bytes from the end of path.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("quota: open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("quota: stat %s: %w", path, err)
	}
	off := max(info.Size()-n, 0)
	b, err := io.ReadAll(io.NewSectionReader(f, off, info.Size()-off))
	if err != nil {
		return nil, fmt.Errorf("quota: read %s: %w", path, err)
	}
	return b, nil
}
