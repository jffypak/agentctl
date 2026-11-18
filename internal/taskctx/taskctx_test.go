package taskctx_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/taskctx"
	"github.com/jffypak/agentctl/internal/testenv"
)

func TestPathIsUnderHomeTasks(t *testing.T) {
	if got, want := taskctx.Path("/h", "demo"), "/h/tasks/demo/context.md"; got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestSessionTextCreatesTheFileAndNamesIt(t *testing.T) {
	home := testenv.TempHome(t)
	text, err := taskctx.SessionText(home, "demo")
	if err != nil {
		t.Fatal(err)
	}
	path := taskctx.Path(home, "demo")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", di.Mode().Perm())
	}
	for _, want := range []string{path, `task "demo"`, "no notes yet"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestSessionTextIncludesNotes(t *testing.T) {
	home := testenv.TempHome(t)
	write(t, home, "demo", "# notes\n\nThe flaky test is TestFoo.\n")
	text, err := taskctx.SessionText(home, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "The flaky test is TestFoo.") || strings.Contains(text, "no notes yet") {
		t.Fatalf("text:\n%s", text)
	}
}

func TestSessionTextLeavesOutALargeFile(t *testing.T) {
	home := testenv.TempHome(t)
	write(t, home, "demo", strings.Repeat("x", taskctx.MaxInline+1))
	text, err := taskctx.SessionText(home, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "xxxx") || !strings.Contains(text, "Read tool") {
		t.Fatalf("large file inlined or not explained:\n%.300s", text)
	}
}

func TestSessionTextLeavesOutInvalidUTF8(t *testing.T) {
	home := testenv.TempHome(t)
	write(t, home, "demo", "ok \xff\xfe bad")
	text, err := taskctx.SessionText(home, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "\xff") || !strings.Contains(text, "UTF-8") {
		t.Fatalf("text:\n%q", text)
	}
}

func TestBadTaskIDIsRefused(t *testing.T) {
	home := testenv.TempHome(t)
	for _, id := range []string{"", ".", "..", "a/b", "../x", "a\x00b"} {
		if _, err := taskctx.SessionText(home, id); err == nil {
			t.Errorf("SessionText(%q) accepted", id)
		}
		if _, err := taskctx.Archive(home, id, time.Now()); err == nil {
			t.Errorf("Archive(%q) accepted", id)
		}
	}
}

func TestArchiveMovesNotesAndFreesTheTaskDir(t *testing.T) {
	home := testenv.TempHome(t)
	write(t, home, "demo", "keep me\n")
	now := time.UnixMilli(1700000000123)
	got, err := taskctx.Archive(home, "demo", now)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "context-archive", "demo~1700000000123.md")
	if got != want {
		t.Fatalf("Archive = %q, want %q", got, want)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != "keep me\n" {
		t.Fatalf("archive = %q, %v", b, err)
	}
	if _, err := os.Lstat(filepath.Dir(taskctx.Path(home, "demo"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("task dir still there: %v", err)
	}
}

func TestArchiveOfTemplateOnlyFileKeepsNothing(t *testing.T) {
	home := testenv.TempHome(t)
	if _, err := taskctx.SessionText(home, "demo"); err != nil { // creates the template
		t.Fatal(err)
	}
	got, err := taskctx.Archive(home, "demo", time.Now())
	if err != nil || got != "" {
		t.Fatalf("Archive = %q, %v; want nothing archived", got, err)
	}
	if _, err := os.Lstat(filepath.Dir(taskctx.Path(home, "demo"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("task dir still there: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "context-archive")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive dir created for nothing: %v", err)
	}
}

func TestArchiveWithoutAFileIsANoOp(t *testing.T) {
	home := testenv.TempHome(t)
	if got, err := taskctx.Archive(home, "demo", time.Now()); err != nil || got != "" {
		t.Fatalf("Archive = %q, %v", got, err)
	}
}

func TestArchiveKeepsOtherFilesInTheTaskDir(t *testing.T) {
	home := testenv.TempHome(t)
	write(t, home, "demo", "notes\n")
	extra := filepath.Join(filepath.Dir(taskctx.Path(home, "demo")), "other.txt")
	if err := os.WriteFile(extra, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := taskctx.Archive(home, "demo", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Fatalf("Archive removed a file it does not own: %v", err)
	}
}

func write(t *testing.T, home, id, body string) {
	t.Helper()
	p := taskctx.Path(home, id)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHasNotes(t *testing.T) {
	home := testenv.TempHome(t)
	if taskctx.HasNotes(home, "demo") {
		t.Error("no file: HasNotes = true")
	}
	if _, err := taskctx.SessionText(home, "demo"); err != nil {
		t.Fatal(err)
	}
	if taskctx.HasNotes(home, "demo") {
		t.Error("template only: HasNotes = true")
	}
	write(t, home, "demo", "a note\n")
	if !taskctx.HasNotes(home, "demo") {
		t.Error("with a note: HasNotes = false")
	}
	write(t, home, "big", strings.Repeat("x", taskctx.MaxInline+1))
	if !taskctx.HasNotes(home, "big") {
		t.Error("large file: HasNotes = false")
	}
}
