package hookingest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/testenv"
)

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// TestExecSpawnerDetaches: the child gets the deliver argv, runs in its own
// session, writes only to the deliver log, and SpawnDeliver does not wait.
func TestExecSpawnerDetaches(t *testing.T) {
	dir := testenv.TempHome(t)
	out := filepath.Join(dir, "argv")
	exe := filepath.Join(dir, "fake-agentctl")
	script := "#!/bin/sh\nsleep 0.3\nprintf '%s\\n' \"$@\" > " + out + ".tmp\nps -o sess= -p $$ >> " + out + ".tmp 2>/dev/null\nmv " + out + ".tmp " + out + "\necho logged\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "logs", DeliverLogName)
	start := time.Now()
	if err := (ExecSpawner{Exe: exe, Env: testenv.NewEnv(t).Strings(), LogPath: logPath}).SpawnDeliver(7, 4242); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 250*time.Millisecond {
		t.Fatalf("SpawnDeliver waited %v for the child", d)
	}
	testenv.WaitFor(t, 0, "spawned deliver", func() (bool, string) {
		_, err := os.Stat(out)
		return err == nil, ""
	})
	b, _ := os.ReadFile(out)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if strings.Join(lines[:5], " ") != "deliver --session 7 --after-pid 4242" {
		t.Fatalf("argv = %q", lines)
	}
	testenv.WaitFor(t, 0, "deliver log", func() (bool, string) {
		b, _ := os.ReadFile(logPath)
		return strings.Contains(string(b), "logged"), string(b)
	})
	if fi, err := os.Stat(logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("deliver log mode: %v", err)
	}
	if err := (ExecSpawner{Exe: "agentctl", LogPath: logPath}).SpawnDeliver(1, 1); err == nil {
		t.Fatal("relative exe accepted")
	}
}
