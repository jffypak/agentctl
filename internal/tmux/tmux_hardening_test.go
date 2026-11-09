package tmux

import (
	"errors"
	"testing"
)

// TestIsNoServerCoversADyingServer: a client that reaches a server which is
// shutting down (still owning the socket, dropping new clients) is told
// "server exited unexpectedly" (tmux 3.x) or "lost server" (older tmux).
// Neither is a probe failure: the server is going away, so both must read as
// ErrNoServer and reconcile reports server_gone (docs/notes/m1-flaky-tests.md).
func TestIsNoServerCoversADyingServer(t *testing.T) {
	for _, tc := range []struct {
		stderr string
		want   bool
	}{
		{"no server running on /tmp/tmux-501/x", true},
		{"error connecting to /tmp/tmux-501/x (No such file or directory)", true},
		{"server exited unexpectedly", true},
		{"lost server", true},
		{"server exited unexpectedly\n", true},
		{"duplicate session: x", false},
		{"can't find session: x", false},
		{"can't find pane: %3", false},
		{"unknown command: lost", false},
		{"", false},
	} {
		if got := isNoServer(firstLine(tc.stderr)); got != tc.want {
			t.Errorf("isNoServer(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
		err := newError([]string{"list-panes"}, tc.stderr, errors.New("exit status 1"))
		if got := errors.Is(err, ErrNoServer); got != tc.want {
			t.Errorf("errors.Is(%q, ErrNoServer) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}
