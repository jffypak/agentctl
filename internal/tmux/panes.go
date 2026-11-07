package tmux

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jffypak/agentctl/internal/domain"
)

// paneFormat lists fixed-shape fields first and the one truly free-form field
// (pane_current_path) last, because tmux escapes control bytes in output but
// passes tabs and backslashes through; SplitN keeps a tab inside the path.
// session_name is safe: tmux vis-encodes names when they are set.
const paneFormat = "#{pid}\t#{session_id}\t#{window_index}\t#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{pane_dead_status}\t#{pane_dead_signal}\t#{session_name}\t#{pane_current_command}\t#{pane_current_path}"

const paneFields = 11

// Pane is one row of list-panes.
type Pane struct {
	ServerPID      int
	SessionID      string
	WindowIndex    int
	PaneID         string
	PanePID        int
	Dead           bool
	DeadStatus     int
	DeadSignal     int
	SessionName    string
	CurrentCommand string
	CurrentPath    string
}

// Snapshot is one consistent view of a server: its pid and every pane.
type Snapshot struct {
	ServerPID int
	Panes     []Pane
}

// SnapshotCommands reads the server pid and every pane in one invocation
// (display-message works on a server with no sessions; list-panes -a then
// fails with "no current target", which parseSnapshot's caller tolerates).
func SnapshotCommands() [][]string {
	return [][]string{{"display-message", "-p", "#{pid}"}, ListPanesCommand()}
}

// ListPanesCommand lists every pane on the server.
func ListPanesCommand() []string { return []string{"list-panes", "-a", "-F", paneFormat} }

// Snapshot probes the server. ErrNoServer (via errors.Is) when the socket is
// missing or nothing answers it.
func (c Client) Snapshot(ctx context.Context) (Snapshot, error) {
	res, err := c.run(ctx, nil, SnapshotCommands()...)
	if err != nil {
		var te *Error
		if errors.As(err, &te) && te.Stderr == "no current target" {
			return parseSnapshot(res.Stdout) // server up, zero sessions
		}
		return Snapshot{}, err
	}
	return parseSnapshot(res.Stdout)
}

// ListPanes returns every pane on the server.
func (c Client) ListPanes(ctx context.Context) ([]Pane, error) {
	s, err := c.Snapshot(ctx)
	return s.Panes, err
}

func parseSnapshot(out []byte) (Snapshot, error) {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return Snapshot{}, fmt.Errorf("tmux: unexpected server pid line %q", lines[0])
	}
	snap := Snapshot{ServerPID: pid}
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		p, err := parsePane(line)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Panes = append(snap.Panes, p)
	}
	return snap, nil
}

func parsePane(line string) (Pane, error) {
	f := strings.SplitN(line, "\t", paneFields)
	if len(f) != paneFields {
		return Pane{}, fmt.Errorf("tmux: unexpected list-panes line %q", line)
	}
	ints := make([]int, 0, 6)
	for _, i := range []int{0, 2, 4, 5, 6, 7} {
		if f[i] == "" && (i == 6 || i == 7) { // dead status/signal are empty while alive
			ints = append(ints, 0)
			continue
		}
		n, err := strconv.Atoi(f[i])
		if err != nil {
			return Pane{}, fmt.Errorf("tmux: unexpected field %d in list-panes line %q", i, line)
		}
		ints = append(ints, n)
	}
	if !strings.HasPrefix(f[1], "$") || !strings.HasPrefix(f[3], "%") {
		return Pane{}, fmt.Errorf("tmux: unexpected ids in list-panes line %q", line)
	}
	return Pane{
		ServerPID: ints[0], SessionID: f[1], WindowIndex: ints[1], PaneID: f[3], PanePID: ints[2],
		Dead: ints[3] == 1, DeadStatus: ints[4], DeadSignal: ints[5],
		SessionName: f[8], CurrentCommand: f[9], CurrentPath: f[10],
	}, nil
}

// Find returns the pane with id paneID.
func (s Snapshot) Find(paneID string) (Pane, bool) {
	for _, p := range s.Panes {
		if p.PaneID == paneID {
			return p, true
		}
	}
	return Pane{}, false
}

// Observe converts a snapshot into the pure observation domain.ClassifyLiveness
// compares against a stored identity.
func (s Snapshot) Observe(paneID string) domain.PaneObservation {
	obs := domain.PaneObservation{ServerReachable: true, ServerPID: s.ServerPID}
	if p, ok := s.Find(paneID); ok {
		obs.Found, obs.PanePID, obs.Dead, obs.DeadStatus, obs.DeadSignal = true, p.PanePID, p.Dead, p.DeadStatus, p.DeadSignal
	}
	return obs
}
