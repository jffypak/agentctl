package tmux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// CaptureAll asks CapturePane for the pane's whole scrollback.
const CaptureAll = -1

// Sentinel capture errors.
var (
	// ErrPaneMismatch: the pane id now belongs to another process or another
	// server (pane-id reuse). The server checked this before capturing, so no
	// text from that pane ever left tmux.
	ErrPaneMismatch = errors.New("tmux: pane identity changed; nothing captured")
	// ErrPaneNotFound: no pane with that id exists on the server.
	ErrPaneNotFound = errors.New("tmux: pane not found")
)

// Markers printed by the branches of the conditional capture.
const (
	captureOKMarker       = "agentctl-capture-ok"
	captureMismatchMarker = "agentctl-capture-mismatch"
	captureMissingMarker  = "agentctl-capture-missing"
)

var paneIDPattern = regexp.MustCompile(`^%[0-9]+$`)

// CaptureTarget is a pane plus the identity it must still have when the
// server captures it.
type CaptureTarget struct {
	PaneID    string // "%N"
	ServerPID int    // the server the identity was captured on
	PanePID   int    // the pane's process at capture time
}

// CaptureCommands builds one invocation that captures a pane only if its
// identity still matches, decided by the server itself:
//
//	if-shell -F -t %N '#{&&:#{==:#{pid},S},#{==:#{pane_pid},P}}' \
//	  'display-message -p agentctl-capture-ok ; capture-pane -p -J -t %N -S <start> -E -' \
//	  "display-message -p -t %N '#{?pane_id,agentctl-capture-mismatch,agentctl-capture-missing}'"
//
// Checking in Go after capturing would already have read the text of a reused
// %N; here a mismatched pane is never captured at all. The command strings
// contain only a validated pane id and integers, never user text. history is
// the number of scrollback lines above the visible screen (0 = screen only,
// CaptureAll = everything). -J joins wrapped lines; no -e, so attributes and
// escape sequences are not emitted (callers still sanitize).
func CaptureCommands(t CaptureTarget, history int) ([][]string, error) {
	if !paneIDPattern.MatchString(t.PaneID) {
		return nil, fmt.Errorf("tmux: invalid pane id %q", t.PaneID)
	}
	if t.ServerPID <= 0 || t.PanePID <= 0 {
		return nil, fmt.Errorf("tmux: capture of %s needs the server pid and pane pid it was bound to", t.PaneID)
	}
	var start string
	switch {
	case history == CaptureAll:
		start = "-"
	case history == 0:
		start = "0"
	case history > 0:
		start = "-" + strconv.Itoa(history)
	default:
		return nil, fmt.Errorf("tmux: invalid capture history %d", history)
	}
	cond := fmt.Sprintf("#{&&:#{==:#{pid},%d},#{==:#{pane_pid},%d}}", t.ServerPID, t.PanePID)
	capture := fmt.Sprintf("display-message -p %s ; capture-pane -p -J -t %s -S %s -E -", captureOKMarker, t.PaneID, start)
	// if-shell and display-message tolerate a missing target (formats then
	// expand empty), so the else branch tells "gone" from "someone else's".
	mismatch := fmt.Sprintf("display-message -p -t %s '#{?pane_id,%s,%s}'", t.PaneID, captureMismatchMarker, captureMissingMarker)
	return [][]string{{"if-shell", "-F", "-t", t.PaneID, cond, capture, mismatch}}, nil
}

// CapturePane returns the raw text of a pane (not sanitized: callers pass it
// through domain.SanitizeTerminalText before display). ErrPaneMismatch when
// the identity no longer matches, ErrPaneNotFound when the pane is gone, and
// ErrNoServer (via errors.Is) when nothing answers the socket. A dead pane
// (remain-on-exit) is still captured: its final output is what matters.
func (c Client) CapturePane(ctx context.Context, t CaptureTarget, history int) (string, error) {
	cmds, err := CaptureCommands(t, history)
	if err != nil {
		return "", err
	}
	res, err := c.run(ctx, nil, cmds...)
	if err != nil {
		var te *Error
		if errors.As(err, &te) && strings.HasPrefix(te.Stderr, "can't find pane") {
			return "", fmt.Errorf("%w: %s", ErrPaneNotFound, t.PaneID)
		}
		return "", err
	}
	marker, text, _ := strings.Cut(string(res.Stdout), "\n")
	switch marker {
	case captureOKMarker:
		return text, nil
	case captureMismatchMarker:
		return "", fmt.Errorf("%w (%s)", ErrPaneMismatch, t.PaneID)
	case captureMissingMarker:
		return "", fmt.Errorf("%w: %s", ErrPaneNotFound, t.PaneID)
	}
	return "", fmt.Errorf("tmux: unexpected capture output for %s", t.PaneID)
}
