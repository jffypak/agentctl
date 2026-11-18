package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jffypak/agentctl/internal/taskctx"
)

// TestDeleteArchivesTaskContext: a delete moves the task's context file to
// context-archive/<task>~<ms>.md, so a new task of the same name starts
// with no notes.
func TestDeleteArchivesTaskContext(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	piNewTask(t, f.ctl, f.repo, "demo")
	home := f.ctl.Paths().Home
	path := taskctx.Path(home, "demo")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("decided: use the v2 API\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	plan, err := f.ctl.PlanDelete(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Join(DeletePlanLines(plan), "\n"); !strings.Contains(lines, "archives context file") {
		t.Fatalf("plan lines lack the archive:\n%s", lines)
	}
	res, err := f.ctl.DeleteTask(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.ContextArchived, filepath.Join(home, "context-archive", "demo~")) {
		t.Fatalf("ContextArchived = %q", res.ContextArchived)
	}
	if b, err := os.ReadFile(res.ContextArchived); err != nil || string(b) != "decided: use the v2 API\n" {
		t.Fatalf("archive = %q, %v", b, err)
	}
	if !strings.Contains(DeleteSummary(res), "archived context to") {
		t.Fatalf("summary = %q", DeleteSummary(res))
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("context file still there: %v", err)
	}
}

// TestDeleteWithoutNotesArchivesNothing: no plan line and no archive when
// the task never saved a note.
func TestDeleteWithoutNotesArchivesNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, sleepAgent)
	ctx := context.Background()
	piNewTask(t, f.ctl, f.repo, "demo")
	plan, err := f.ctl.PlanDelete(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Join(DeletePlanLines(plan), "\n"); strings.Contains(lines, "context") {
		t.Fatalf("plan mentions a context file:\n%s", lines)
	}
	res, err := f.ctl.DeleteTask(ctx, "demo")
	if err != nil || res.ContextArchived != "" {
		t.Fatalf("ContextArchived = %q, %v", res.ContextArchived, err)
	}
}
