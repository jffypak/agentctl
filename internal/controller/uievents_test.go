package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
	"github.com/jffypak/agentctl/internal/testenv"
)

func TestUIEventsCarryTheRunID(t *testing.T) {
	t.Parallel()
	ctl := openController(t, testenv.NewEnv(t))
	ctx := context.Background()
	run, err := ctl.UIOpened(ctx)
	if err != nil || len(run) != 16 {
		t.Fatalf("UIOpened = %q, %v", run, err)
	}
	other, _ := ctl.UIOpened(ctx)
	if other == run {
		t.Fatal("run ids repeat")
	}
	if err := ctl.UIAction(ctx, run, UIActionAttach, "demo"); err != nil {
		t.Fatal(err)
	}
	if err := ctl.UIClosed(ctx, run); err != nil {
		t.Fatal(err)
	}
	evs, err := ctl.EventsSince(ctx, 0, 0)
	if err != nil || len(evs) != 4 {
		t.Fatalf("events = %v, %v", evs, err)
	}
	want := []struct {
		typ     string
		task    string
		payload map[string]string
	}{
		{domain.EventUIOpened, "", map[string]string{"ui_run_id": run}},
		{domain.EventUIOpened, "", map[string]string{"ui_run_id": other}},
		{domain.EventUIAction, "demo", map[string]string{"ui_run_id": run, "action": "attach", "task_id": "demo"}},
		{domain.EventUIClosed, "", map[string]string{"ui_run_id": run}},
	}
	for i, w := range want {
		var got map[string]string
		if err := json.Unmarshal(evs[i].Payload, &got); err != nil {
			t.Fatal(err)
		}
		if evs[i].Type != w.typ || evs[i].TaskID != w.task || len(got) != len(w.payload) {
			t.Fatalf("event %d = %+v %v", i, evs[i], got)
		}
		for k, v := range w.payload {
			if got[k] != v {
				t.Fatalf("event %d payload %v, want %v", i, got, w.payload)
			}
		}
	}
}

func TestUIEventValidation(t *testing.T) {
	t.Parallel()
	ctl := openController(t, testenv.NewEnv(t))
	ctx := context.Background()
	if err := ctl.UIAction(ctx, "", UIActionAttach, "x"); err == nil {
		t.Fatal("empty run id accepted")
	}
	if err := ctl.UIAction(ctx, "r", "dance", "x"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if err := ctl.UIClosed(ctx, ""); err == nil {
		t.Fatal("empty run id accepted for ui.closed")
	}
	if len(UIActions()) != 7 {
		t.Fatalf("actions = %v", UIActions())
	}
}

func TestComputeStats(t *testing.T) {
	t0 := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	run := func(id string, open, first int) store.UIRunStat {
		r := store.UIRunStat{RunID: id, OpenedAt: t0.Add(time.Duration(open) * time.Second)}
		if first >= 0 {
			r.FirstActionAt, r.HasAction = t0.Add(time.Duration(first)*time.Second), true
		}
		return r
	}
	cases := []struct {
		name     string
		runs     []store.UIRunStat
		attaches int
		prompts  int
		want     Stats
		ratio    float64
		ratioOK  bool
	}{
		{name: "empty", want: Stats{}},
		{name: "odd", runs: []store.UIRunStat{run("a", 0, 10), run("b", 0, -1), run("c", 100, 130), run("d", 0, 50)},
			attaches: 3, prompts: 6, ratio: 0.5, ratioOK: true,
			want: Stats{Runs: 4, RunsWithAction: 3, RunsWithoutAction: 1, MedianOpenToFirstAction: 30 * time.Second, Attaches: 3, Prompts: 6}},
		{name: "even", runs: []store.UIRunStat{run("a", 0, 10), run("b", 0, 20)},
			attaches: 2, ratioOK: false,
			want: Stats{Runs: 2, RunsWithAction: 2, MedianOpenToFirstAction: 15 * time.Second, Attaches: 2}},
		{name: "none acted", runs: []store.UIRunStat{run("a", 0, -1)},
			want: Stats{Runs: 1, RunsWithoutAction: 1}},
	}
	for _, c := range cases {
		got := computeStats(c.runs, c.attaches, c.prompts)
		if got != c.want {
			t.Errorf("%s: stats = %+v, want %+v", c.name, got, c.want)
		}
		if r, ok := got.AttachesPerPrompt(); ok != c.ratioOK || r != c.ratio {
			t.Errorf("%s: ratio = %v, %v", c.name, r, ok)
		}
	}
}

func TestStatsReadsInstrumentationEvents(t *testing.T) {
	t.Parallel()
	ctl := openController(t, testenv.NewEnv(t))
	ctx := context.Background()
	run, _ := ctl.UIOpened(ctx)
	if err := ctl.UIAction(ctx, run, UIActionAttach, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.UIOpened(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := ctl.store.Events().Append(ctx, store.EventInput{Type: domain.EventMessageQueued}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := ctl.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.Runs != 2 || s.RunsWithAction != 1 || s.RunsWithoutAction != 1 || s.Attaches != 1 || s.Prompts != 2 {
		t.Fatalf("stats = %+v", s)
	}
}
