package tmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvSocket overrides socket resolution (tests point it at an isolated server).
const EnvSocket = "AGENTCTL_TMUX_SOCKET"

// ServerPID returns the pid of the server on the client's socket.
func (c Client) ServerPID(ctx context.Context) (int, error) {
	out, err := c.display(ctx, "#{pid}")
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(out)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("tmux: unexpected server pid %q", out)
	}
	return pid, nil
}

// SocketPath returns the server's own socket path, canonicalized.
func (c Client) SocketPath(ctx context.Context) (string, error) {
	out, err := c.display(ctx, "#{socket_path}")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(out) {
		return "", fmt.Errorf("tmux: unexpected socket path %q", out)
	}
	return CanonicalSocket(out), nil
}

func (c Client) display(ctx context.Context, format string) (string, error) {
	res, err := c.run(ctx, nil, []string{"display-message", "-p", format})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// ResolveSocket picks the server agentctl launches into: $AGENTCTL_TMUX_SOCKET,
// then the socket in $TMUX, then the default server
// ($TMUX_TMPDIR or /tmp)/tmux-<uid>/default. The result is absolute and
// canonical (symlinks resolved, macOS /tmp -> /private/tmp).
func ResolveSocket(getenv func(string) string, uid int) (string, error) {
	if p := getenv(EnvSocket); p != "" {
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("%s=%q must be an absolute path", EnvSocket, p)
		}
		return CanonicalSocket(p), nil
	}
	if p := SocketFromTMUX(getenv("TMUX")); p != "" {
		return CanonicalSocket(p), nil
	}
	dir := getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return CanonicalSocket(filepath.Join(dir, "tmux-"+strconv.Itoa(uid), "default")), nil
}

// SocketFromTMUX extracts the socket path from a $TMUX value
// ("<path>,<server pid>,<session index>"). The path may itself contain commas,
// so the two numeric fields are split off the right.
func SocketFromTMUX(v string) string {
	i := strings.LastIndex(v, ",")
	if i <= 0 {
		return ""
	}
	j := strings.LastIndex(v[:i], ",")
	if j <= 0 {
		return ""
	}
	if _, err := strconv.Atoi(v[j+1 : i]); err != nil {
		return ""
	}
	return v[:j]
}

// CanonicalSocket makes a socket path absolute and resolves symlinks. When the
// socket does not exist yet its directory is resolved instead (or the closest
// existing ancestor), so the result is stable before and after the server starts.
func CanonicalSocket(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	dir, base := filepath.Split(abs)
	dir = filepath.Clean(dir)
	if _, err := os.Lstat(dir); err != nil && dir != "/" {
		return filepath.Join(CanonicalSocket(dir), base)
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(real, base)
	}
	return abs
}

// SameSocket compares two socket paths after canonicalization.
func SameSocket(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return CanonicalSocket(a) == CanonicalSocket(b)
}
