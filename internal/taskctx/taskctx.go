// Package taskctx owns each task's context file,
// <home>/tasks/<task>/context.md. The file lives as long as the task: every
// Claude Code session of the task (startup, resume, /clear, compaction) gets
// its path and contents from the SessionStart hook (internal/hookingest), so
// notes saved there survive across sessions. A confirmed delete moves the
// file to <home>/context-archive/<task>~<ms>.md (internal/controller). A plain
// close leaves it in place.
//
// The file is outside the worktree on purpose: a delete removes the worktree,
// and the file can never be committed by accident. The launch settings allow
// Claude Code to read and edit everything under <home>/tasks without a
// permission prompt (adapter/claude settings.go).
package taskctx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jffypak/agentctl/internal/paths"
)

// FileName is the context file's name inside the task directory.
const FileName = "context.md"

// MaxInline is the largest file, in bytes, whose contents SessionText
// includes. A larger file is named but not shown, so one big file cannot
// fill every new session's context.
const MaxInline = 8 << 10

// Path is the task's context file.
func Path(home, taskID string) string {
	return filepath.Join(paths.ForHome(home).Tasks, taskID, FileName)
}

// SessionText makes sure the task's context file exists and returns the text
// the SessionStart hook prints: what the file is for, its path, and its
// contents when they are short, valid UTF-8 and not only the template.
func SessionText(home, taskID string) (string, error) {
	if err := checkID(taskID); err != nil {
		return "", err
	}
	path, err := ensure(home, taskID)
	if err != nil {
		return "", err
	}
	body, size, err := readSmall(path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "agentctl task context for task %q: %s\n", taskID, path)
	b.WriteString("This file persists across every session of this task (restarts, /clear, compaction) until the task is deleted. " +
		"Save notes that a later session needs there: decisions, progress, open questions. Keep it short and current.\n\n")
	switch {
	case size > MaxInline:
		fmt.Fprintf(&b, "The file is %d bytes, over the %d KB inline limit, so its contents are not shown. Read it with the Read tool.\n", size, MaxInline>>10)
	case !utf8.ValidString(body):
		b.WriteString("The file is not valid UTF-8, so its contents are not shown. Read it with the Read tool.\n")
	case !hasNotes(body, taskID):
		b.WriteString("The file has no notes yet.\n")
	default:
		b.WriteString("Current contents:\n\n")
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// Archive moves the task's context file to
// <home>/context-archive/<task>~<ms of now>.md and returns that path. A
// missing file, or one that holds only the template, is removed without an
// archive and Archive returns "". The task directory is removed when it is
// empty afterwards; other files in it are kept.
func Archive(home, taskID string, now time.Time) (string, error) {
	if err := checkID(taskID); err != nil {
		return "", err
	}
	path := Path(home, taskID)
	dir := filepath.Dir(path)
	body, size, err := readSmall(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		_ = os.Remove(dir)
		return "", nil
	case err != nil:
		return "", err
	}
	if size <= MaxInline && !hasNotes(body, taskID) {
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("remove context file: %w", err)
		}
		_ = os.Remove(dir)
		return "", nil
	}
	archiveDir := paths.ForHome(home).ContextArchive
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", archiveDir, err)
	}
	dest := filepath.Join(archiveDir, taskID+"~"+strconv.FormatInt(now.UnixMilli(), 10)+".md")
	if err := os.Rename(path, dest); err != nil {
		return "", fmt.Errorf("archive context file: %w", err)
	}
	_ = os.Remove(dir) // fails, harmlessly, when other files remain
	return dest, nil
}

// HasNotes reports whether the task's context file holds more than the
// template, that is, whether Archive would keep it. An unreadable file
// counts as notes, so a delete plan never hides it.
func HasNotes(home, taskID string) bool {
	if checkID(taskID) != nil {
		return false
	}
	body, size, err := readSmall(Path(home, taskID))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false
	case err != nil:
		return true
	}
	return size > MaxInline || hasNotes(body, taskID)
}

// template is what a new context file holds.
func template(taskID string) string {
	return "# Task context: " + taskID + "\n\n" +
		"<!-- agentctl keeps this file while the task exists and shows it to every new session. Save notes below. -->\n"
}

// hasNotes reports whether body holds more than the template and white space.
func hasNotes(body, taskID string) bool {
	t := strings.TrimSpace(body)
	return t != "" && t != strings.TrimSpace(template(taskID))
}

// ensure creates the task directory (0700) and the file (0600, the template)
// when they are missing, and returns the file's path.
func ensure(home, taskID string) (string, error) {
	path := Path(home, taskID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create task context dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	switch {
	case errors.Is(err, os.ErrExist):
		return path, nil
	case err != nil:
		return "", fmt.Errorf("create context file: %w", err)
	}
	_, werr := f.WriteString(template(taskID))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", fmt.Errorf("write context file: %w", werr)
	}
	return path, nil
}

// readSmall returns the file's size and, when it is at most MaxInline bytes,
// its contents.
func readSmall(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, fmt.Errorf("context file %s is not a regular file", path)
	}
	if fi.Size() > MaxInline {
		return "", fi.Size(), nil
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxInline+1))
	if err != nil {
		return "", 0, err
	}
	return string(b), int64(len(b)), nil
}

// checkID refuses a task id that is not one path element.
func checkID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return fmt.Errorf("invalid task id %q for a context file", id)
	}
	return nil
}
