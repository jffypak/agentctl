package testenv

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
)

// built caches one `go build` per test binary.
type built struct {
	once sync.Once
	path string
	err  error
}

var agentctlBin, fakeAgentBin built

// BuildAgentctl builds ./cmd/agentctl once per test binary and returns its
// absolute path. Integration tests run it as a real process: hooks invoke it
// exactly as Claude Code would. Skips when the go tool is missing.
func BuildAgentctl(t testing.TB) string {
	t.Helper()
	return buildOnce(t, &agentctlBin, "agentctl", "./cmd/agentctl", "help")
}

// BuildFakeAgent builds ./cmd/fakeagent (the scripted Claude Code stand-in)
// once per test binary and returns its absolute path.
func BuildFakeAgent(t testing.TB) string {
	t.Helper()
	return buildOnce(t, &fakeAgentBin, "fakeagent", "./cmd/fakeagent", "-h")
}

// buildOnce builds pkg, then runs it once with warmArg (a stateless flag that
// exits at once) and an empty environment: the first exec of a freshly built
// binary pays macOS's one-time code scan (hundreds of ms, serialized under
// load), which would otherwise land inside the first timed hook or launch.
func buildOnce(t testing.TB, b *built, name, pkg, warmArg string) string {
	t.Helper()
	goBin := RequireTool(t, "go")
	b.once.Do(func() {
		b.path, b.err = goBuild(goBin, name, pkg)
		if b.err == nil {
			warm := exec.Command(b.path, warmArg)
			warm.Env, warm.Dir = []string{}, filepath.Dir(b.path)
			_ = warm.Run() // the exit status is irrelevant
		}
	})
	if b.err != nil {
		t.Fatalf("build %s: %v", pkg, b.err)
	}
	return b.path
}

// goBuild compiles pkg into a per-user, per-checkout directory under the
// system temp dir (not a t.TempDir: the binary outlives the first test that
// asks for it). It builds to a temp name and renames, so test binaries of
// several packages building concurrently never exec a half-written file, and
// each run overwrites the previous binary instead of leaving new ones behind.
func goBuild(goBin, name, pkg string) (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	dir := filepath.Join(os.TempDir(), "agentctl-testbin-"+strconv.Itoa(os.Getuid()), hex.EncodeToString(sum[:6]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return "", err
	}
	_ = tmp.Close()
	cmd := exec.Command(goBin, "build", "-o", tmp.Name(), pkg)
	cmd.Dir = root
	// The go tool needs the developer's GOPATH/GOCACHE; nothing it runs touches
	// tmux, ~/.claude, or agentctl state.
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("go build %s: %v\n%s", pkg, err, out)
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(tmp.Name(), final); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return final, nil
}

// moduleRoot is the directory holding go.mod: two levels above this file.
func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("testenv: cannot locate the module root")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", fmt.Errorf("testenv: no go.mod at %s: %w", root, err)
	}
	return root, nil
}
