package hookingest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jffypak/agentctl/internal/domain"
)

// ErrorLogName is the file under <home>/logs that hook errors are appended to.
const ErrorLogName = "hook-errors.log"

// maxErrorLog is the size at which the log is rotated to hook-errors.log.1
// (one generation kept), so a misbehaving setup cannot grow it unbounded.
const maxErrorLog = 1 << 20

// errorLog appends one line per error:
//
//	2026-11-07T12:00:00.000Z pid=123 event=Stop task=demo session=4: <error>
//
// Before the home is known (or when it does not exist) it falls back to one
// line on stderr; it never writes to stdout.
type errorLog struct {
	mu     sync.Mutex
	path   string
	stderr io.Writer
	now    func() time.Time
	pid    int
}

func (l *errorLog) setPath(p string) {
	l.mu.Lock()
	l.path = p
	l.mu.Unlock()
}

func (l *errorLog) write(o Outcome, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := fmt.Sprintf("%s pid=%d event=%s task=%s session=%d: %s\n",
		l.now().UTC().Format("2006-01-02T15:04:05.000Z07:00"), l.pid,
		orDash(o.Event), orDash(o.TaskID), o.SessionID, clean(err.Error()))
	if l.path != "" {
		if werr := appendLine(l.path, line); werr == nil {
			return
		}
	}
	fmt.Fprint(l.stderr, "agentctl hook: "+line)
}

func appendLine(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxErrorLog {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return clean(s)
}

// clean makes hook-supplied text safe for a one-line log entry.
func clean(s string) string {
	return strings.Join(strings.Fields(domain.SanitizeTerminalText(s)), " ")
}
