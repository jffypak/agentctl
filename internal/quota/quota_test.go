package quota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Unix(1794550000, 0)

func TestParseClaudeStatus(t *testing.T) {
	in := `{"session_id":"x","rate_limits":{"five_hour":{"used_percentage":42,"resets_at":1794557822},"seven_day":{"used_percentage":18.5,"resets_at":1794841357}}}`
	snap, ok := ParseClaudeStatus([]byte(in), now)
	if !ok || snap.Agent != AgentClaude || !snap.CapturedAt.Equal(now) {
		t.Fatalf("snap = %+v ok=%v", snap, ok)
	}
	if snap.FiveHour.UsedPercent != 42 || snap.FiveHour.ResetsAt.Unix() != 1794557822 {
		t.Fatalf("five hour = %+v", snap.FiveHour)
	}
	if snap.Weekly.UsedPercent != 18.5 || snap.Weekly.ResetsAt.Unix() != 1794841357 {
		t.Fatalf("weekly = %+v", snap.Weekly)
	}
}

func TestParseClaudeStatusAbsent(t *testing.T) {
	for name, in := range map[string]string{
		"no rate_limits":  `{"session_id":"x","model":{"id":"claude"}}`,
		"empty object":    `{"rate_limits":{}}`,
		"no percentage":   `{"rate_limits":{"five_hour":{"resets_at":1}}}`,
		"invalid json":    `{"rate_limits":`,
		"empty input":     ``,
		"rate_limits nil": `{"rate_limits":null}`,
	} {
		if snap, ok := ParseClaudeStatus([]byte(in), now); ok {
			t.Errorf("%s: ok with %+v", name, snap)
		}
	}
	// one window alone is enough
	snap, ok := ParseClaudeStatus([]byte(`{"rate_limits":{"seven_day":{"used_percentage":3,"resets_at":1794841357}}}`), now)
	if !ok || snap.FiveHour != nil || snap.Weekly == nil {
		t.Fatalf("weekly only: %+v ok=%v", snap, ok)
	}
}

func TestSaveAndReadClaude(t *testing.T) {
	home := t.TempDir()
	if _, ok, err := ReadClaude(home); ok || err != nil {
		t.Fatalf("empty home: ok=%v err=%v", ok, err)
	}
	snap, _ := ParseClaudeStatus([]byte(`{"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":1794557822}}}`), now)
	if err := SaveClaude(home, snap); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadClaude(home)
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if got.FiveHour.UsedPercent != 42 || got.FiveHour.ResetsAt.Unix() != 1794557822 || got.Weekly != nil || !got.CapturedAt.Equal(now) {
		t.Fatalf("got = %+v", got)
	}
	fi, err := os.Stat(ClaudeFile(home))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v (%v)", fi.Mode(), err)
	}
	ents, _ := os.ReadDir(filepath.Dir(ClaudeFile(home)))
	if len(ents) != 1 {
		t.Fatalf("temp files left: %v", ents)
	}
}

func TestWindowLeft(t *testing.T) {
	w := Window{UsedPercent: 42, ResetsAt: now.Add(time.Hour)}
	if got := w.LeftPercent(now); got != 58 {
		t.Fatalf("left = %v", got)
	}
	if got := (Window{UsedPercent: 104, ResetsAt: now.Add(time.Hour)}).LeftPercent(now); got != 0 {
		t.Fatalf("over limit left = %v", got)
	}
	if !w.Reset(now.Add(time.Hour)) || w.LeftPercent(now.Add(2*time.Hour)) != 100 {
		t.Fatal("a passed reset time should count as fully available")
	}
	if (Window{UsedPercent: 10}).Reset(now) {
		t.Fatal("unknown reset time counted as reset")
	}
}

const codexEvent = `{"timestamp":"2026-11-13T03:22:04.603Z","ordinal":9,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":5.0,"window_minutes":300,"resets_at":1794557822},"secondary":{"used_percent":16.0,"window_minutes":10080,"resets_at":1794841357},"plan_type":"team"}}}`

func TestParseCodexRollout(t *testing.T) {
	older := strings.Replace(codexEvent, `"used_percent":5.0`, `"used_percent":1.0`, 1)
	lines := strings.Join([]string{
		`ent of a truncated line","rate_limits"}`,
		older,
		`{"timestamp":"2026-11-13T03:22:05Z","type":"response_item","payload":{"type":"message"}}`,
		codexEvent,
		`{"timestamp":"2026-11-13T03:22:06Z","type":"event_msg","payload":{"type":"token_count","rate_limits":null}}`,
		``,
	}, "\n")
	snap, ok := ParseCodexRollout([]byte(lines))
	if !ok || snap.Agent != AgentCodex {
		t.Fatalf("snap = %+v ok=%v", snap, ok)
	}
	if snap.FiveHour.UsedPercent != 5 || snap.FiveHour.ResetsAt.Unix() != 1794557822 {
		t.Fatalf("five hour = %+v (want the newest event)", snap.FiveHour)
	}
	if snap.Weekly.UsedPercent != 16 || snap.Weekly.ResetsAt.Unix() != 1794841357 {
		t.Fatalf("weekly = %+v", snap.Weekly)
	}
	if want, _ := time.Parse(time.RFC3339, "2026-11-13T03:22:04.603Z"); !snap.CapturedAt.Equal(want) {
		t.Fatalf("captured = %v", snap.CapturedAt)
	}
}

func TestParseCodexRolloutSwappedWindows(t *testing.T) {
	// windows are placed by length, so primary/secondary order does not matter
	ev := `{"timestamp":"2026-11-13T03:22:04Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":16,"window_minutes":10080,"resets_at":2},"secondary":{"used_percent":5,"window_minutes":300,"resets_at":1}}}}`
	snap, ok := ParseCodexRollout([]byte(ev))
	if !ok || snap.FiveHour.UsedPercent != 5 || snap.Weekly.UsedPercent != 16 {
		t.Fatalf("snap = %+v ok=%v", snap, ok)
	}
	other := strings.Replace(codexEvent, `"limit_id":"codex"`, `"limit_id":"other-model"`, 1)
	if _, ok := ParseCodexRollout([]byte(other)); ok {
		t.Fatal("a non-codex limit was used")
	}
}

func writeRollout(t *testing.T, path, body string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func TestReadCodexPicksNewestRolloutWithData(t *testing.T) {
	home := t.TempDir()
	if _, ok, err := ReadCodex(home); ok || err != nil {
		t.Fatalf("no sessions dir: ok=%v err=%v", ok, err)
	}
	if _, ok, err := ReadCodex(""); ok || err != nil {
		t.Fatalf("no codex home: ok=%v err=%v", ok, err)
	}
	day := filepath.Join(home, "sessions", "2026", "11", "12")
	oldDay := filepath.Join(home, "sessions", "2026", "10", "31")
	writeRollout(t, filepath.Join(oldDay, "rollout-old.jsonl"),
		strings.Replace(codexEvent, `"used_percent":5.0`, `"used_percent":70`, 1)+"\n", now.Add(-48*time.Hour))
	writeRollout(t, filepath.Join(day, "rollout-a.jsonl"), codexEvent+"\n", now.Add(-time.Hour))
	// the newest file is a brand-new session with no token_count yet
	writeRollout(t, filepath.Join(day, "rollout-b.jsonl"), `{"type":"session_meta","payload":{}}`+"\n", now)
	writeRollout(t, filepath.Join(day, "notes.txt"), codexEvent, now.Add(time.Hour))

	snap, ok, err := ReadCodex(home)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if snap.FiveHour.UsedPercent != 5 {
		t.Fatalf("five hour = %+v, want rollout-a's", snap.FiveHour)
	}
}

func TestReadTailSkipsToTheEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-x.jsonl")
	writeRollout(t, path, strings.Repeat("x", codexTail+100)+"\n"+codexEvent+"\n", now)
	b, err := readTail(path, codexTail)
	if err != nil || len(b) != codexTail {
		t.Fatalf("len = %d err=%v", len(b), err)
	}
	if _, ok := ParseCodexRollout(b); !ok {
		t.Fatal("event at the end not found")
	}
}

func TestCodexHome(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := CodexHome(env(map[string]string{"CODEX_HOME": "/c", "HOME": "/h"})); got != "/c" {
		t.Fatalf("CODEX_HOME: %q", got)
	}
	if got := CodexHome(env(map[string]string{"CODEX_HOME": "rel", "HOME": "/h"})); got != "/h/.codex" {
		t.Fatalf("HOME fallback: %q", got)
	}
	if got := CodexHome(env(nil)); got != "" {
		t.Fatalf("nothing: %q", got)
	}
}
