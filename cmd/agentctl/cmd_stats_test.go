package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/paths"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
)

// tiSeedEvents writes synthetic instrumentation events at chosen offsets
// (seconds after a base time) into env's registry.
func tiSeedEvents(t *testing.T, env testenv.Env, events []struct {
	at           int
	typ, payload string
}) {
	t.Helper()
	p, err := paths.Resolve(env.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), p.DB, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	base := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	for _, e := range events {
		if _, err := st.DB().Exec(`INSERT INTO events (ts, type, payload) VALUES (?, ?, ?)`,
			base.Add(time.Duration(e.at)*time.Second).UnixMilli(), e.typ, e.payload); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStatsTextAndJSONOverSyntheticEvents(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	tiSeedEvents(t, env, []struct {
		at           int
		typ, payload string
	}{
		{0, "ui.opened", `{"ui_run_id":"a"}`},
		{12, "ui.action", `{"ui_run_id":"a","action":"attach","task_id":"t1"}`}, // a: 12s
		{20, "ui.closed", `{"ui_run_id":"a"}`},
		{100, "ui.opened", `{"ui_run_id":"b"}`}, // b: no action
		{130, "ui.closed", `{"ui_run_id":"b"}`},
		{200, "ui.opened", `{"ui_run_id":"c"}`},
		{245, "ui.action", `{"ui_run_id":"c","action":"prompt","task_id":"t2"}`}, // c: 45s
		{246, "message.queued", `{}`},
		{250, "ui.action", `{"ui_run_id":"c","action":"attach","task_id":"t2"}`},
		{300, "ui.opened", `{"ui_run_id":"d"}`},
		{317, "ui.action", `{"ui_run_id":"d","action":"diff","task_id":"t3"}`}, // d: 17s
		{400, "message.queued", `{}`},
		{401, "message.queued", `{}`},
		{402, "message.queued", `{}`},
	})
	r := runCmd(t, env, t.TempDir(), "stats")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("stats = %+v", r)
	}
	want := []string{
		"TUI runs                      4",
		"open → first action (median)  17s  (n=3, target < 60s)",
		"runs with no action           1",
		"attaches per prompt           0.50  (2 attaches / 4 prompts)",
	}
	if got := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stats text =\n%s\nwant\n%s", r.stdout, strings.Join(want, "\n"))
	}

	r = runCmd(t, env, t.TempDir(), "stats", "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || r.code != 0 {
		t.Fatalf("stats --json = %v\n%s", err, r.stdout)
	}
	wantJSON := map[string]any{
		"schema_version": 1.0, "runs": 4.0, "runs_with_action": 3.0, "runs_without_action": 1.0,
		"median_open_to_first_action_ms": 17000.0, "median_open_to_first_action_seconds": 17.0,
		"attaches": 2.0, "prompts": 4.0, "attaches_per_prompt": 0.5,
	}
	if len(doc) != len(wantJSON) {
		t.Fatalf("keys = %v", doc)
	}
	for k, v := range wantJSON {
		if doc[k] != v {
			t.Errorf("%s = %v, want %v", k, doc[k], v)
		}
	}
}

func TestStatsWithNoDataUsesDashesAndNulls(t *testing.T) {
	t.Parallel()
	env := testenv.NewEnv(t)
	r := runCmd(t, env, t.TempDir(), "stats")
	if r.code != 0 || !strings.Contains(r.stdout, "open → first action (median)  —  (n=0, target < 60s)") ||
		!strings.Contains(r.stdout, "attaches per prompt           —  (0 attaches / 0 prompts)") {
		t.Fatalf("stats = %+v", r)
	}
	r = runCmd(t, env, t.TempDir(), "stats", "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"median_open_to_first_action_ms", "median_open_to_first_action_seconds", "attaches_per_prompt"} {
		if v, ok := doc[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
	if r := runCmd(t, env, t.TempDir(), "stats", "extra"); r.code != 2 || !strings.Contains(r.stderr, "usage: agentctl stats") {
		t.Fatalf("stats extra = %+v", r)
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0s", 14500 * time.Millisecond: "14.5s", 17 * time.Second: "17s", 59940 * time.Millisecond: "59.9s",
		125 * time.Second: "2m5s", time.Hour + 2*time.Second: "1h0m2s",
	} {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
