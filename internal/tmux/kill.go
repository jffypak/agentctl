package tmux

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jffypak/agentctl/internal/domain"
)

// KillVerifiedSessionCommand builds the only session-killing command agentctl
// sends (archtest keeps kill vocabulary in this file and claude/stop.go). The
// identity check and the kill run inside the server as one command, so a
// server restart that reuses $N/%N between a caller's own verification and
// the kill can never take down someone else's pane:
//
//	if-shell -F -t %P '<server pid, session id, pane id and pane pid all match>' "kill-session -t '$S'"
//
// The guard compares the pane id itself too: tmux evaluates the format in
// some other context when the -t pane is missing, and that must never match.
// The ids are validated strictly ($N / %N, positive pids) because the
// kill-session string is parsed again by tmux; the session id is single-quoted
// so tmux does not expand "$N" as an environment variable.
func KillVerifiedSessionCommand(id domain.Identity) ([]string, error) {
	if !isTmuxID(id.SessionID, '$') || !isTmuxID(id.PaneID, '%') || id.ServerPID <= 0 || id.PanePID <= 0 {
		return nil, fmt.Errorf("tmux: refusing to kill: incomplete or malformed identity %+v", id)
	}
	guard := fmt.Sprintf("#{&&:#{==:#{pid},%d},#{&&:#{==:#{session_id},%s},#{&&:#{==:#{pane_id},%s},#{==:#{pane_pid},%d}}}}",
		id.ServerPID, id.SessionID, id.PaneID, id.PanePID)
	return []string{"if-shell", "-F", "-t", id.PaneID, guard, "kill-session -t '" + id.SessionID + "'"}, nil
}

// KillVerifiedSession kills the tmux session containing id's pane if, and
// only if, the identity still matches exactly. A mismatch or a missing pane is
// not an error (nothing is killed); callers confirm the outcome with a fresh
// snapshot.
func (c Client) KillVerifiedSession(ctx context.Context, id domain.Identity) error {
	cmd, err := KillVerifiedSessionCommand(id)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, nil, cmd)
	return err
}

// isTmuxID reports whether s is prefix followed by one or more digits.
func isTmuxID(s string, prefix byte) bool {
	if len(s) < 2 || s[0] != prefix {
		return false
	}
	_, err := strconv.ParseUint(s[1:], 10, 32)
	return err == nil && s[1] != '+'
}
