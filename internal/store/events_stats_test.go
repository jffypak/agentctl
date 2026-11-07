package store

import (
	"context"
	"testing"
	"time"
)

func insertEventAt(t *testing.T, st *Store, at time.Time, typ, payload string) {
	t.Helper()
	if _, err := st.DB().Exec(`INSERT INTO events (ts, type, payload) VALUES (?, ?, ?)`, ms(at), typ, payload); err != nil {
		t.Fatal(err)
	}
}

func TestUIRunsPairsEachOpenWithItsFirstLaterAction(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }

	// run a: opened at 0, actions at 12 and 30 -> first action 12
	insertEventAt(t, st, at(0), "ui.opened", `{"ui_run_id":"a"}`)
	// run b: opened at 5, no action, closed
	insertEventAt(t, st, at(5), "ui.opened", `{"ui_run_id":"b"}`)
	insertEventAt(t, st, at(12), "ui.action", `{"ui_run_id":"a","action":"attach","task_id":"t1"}`)
	insertEventAt(t, st, at(20), "ui.closed", `{"ui_run_id":"b"}`)
	insertEventAt(t, st, at(30), "ui.action", `{"ui_run_id":"a","action":"prompt","task_id":"t1"}`)
	// an action of an unknown run and a non-ui event are ignored
	insertEventAt(t, st, at(31), "ui.action", `{"ui_run_id":"zzz","action":"attach","task_id":"t1"}`)
	insertEventAt(t, st, at(32), "message.queued", `{}`)
	// run c: opened at 40, action at 41
	insertEventAt(t, st, at(40), "ui.opened", `{"ui_run_id":"c"}`)
	insertEventAt(t, st, at(41), "ui.action", `{"ui_run_id":"c","action":"attach","task_id":"t2"}`)

	runs, err := st.Events().UIRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []UIRunStat{
		{RunID: "a", OpenedAt: at(0), FirstActionAt: at(12), HasAction: true},
		{RunID: "b", OpenedAt: at(5)},
		{RunID: "c", OpenedAt: at(40), FirstActionAt: at(41), HasAction: true},
	}
	if len(runs) != len(want) {
		t.Fatalf("runs = %+v", runs)
	}
	for i := range want {
		g, w := runs[i], want[i]
		if g.RunID != w.RunID || !g.OpenedAt.Equal(w.OpenedAt) || g.HasAction != w.HasAction || !g.FirstActionAt.Equal(w.FirstActionAt) {
			t.Errorf("run %d = %+v, want %+v", i, g, w)
		}
	}

	if n, err := st.Events().CountUIActions(ctx, "attach"); err != nil || n != 3 {
		t.Fatalf("attach actions = %d, %v; want 3", n, err)
	}
	if n, err := st.Events().CountUIActions(ctx, "prompt"); err != nil || n != 1 {
		t.Fatalf("prompt actions = %d, %v; want 1", n, err)
	}
	if n, err := st.Events().CountType(ctx, "message.queued"); err != nil || n != 1 {
		t.Fatalf("message.queued = %d, %v; want 1", n, err)
	}
}

func TestUIRunsEmptyAndMalformedPayloads(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	runs, err := st.Events().UIRuns(ctx)
	if err != nil || len(runs) != 0 {
		t.Fatalf("empty db runs = %+v, %v", runs, err)
	}
	t0 := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	// an opened event without a run id cannot be paired and is skipped
	insertEventAt(t, st, t0, "ui.opened", `{}`)
	insertEventAt(t, st, t0, "ui.opened", `not json`)
	insertEventAt(t, st, t0, "ui.action", `not json`)
	if n, err := st.Events().CountUIActions(ctx, "attach"); err != nil || n != 0 {
		t.Fatalf("malformed actions = %d, %v", n, err)
	}
	runs, err = st.Events().UIRuns(ctx)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs without ids = %+v, %v", runs, err)
	}
}
