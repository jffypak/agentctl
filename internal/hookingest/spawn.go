package hookingest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// DeliverLogName is the file under <home>/logs that a spawned deliver writes to.
const DeliverLogName = "deliver.log"

// Spawner starts a detached `agentctl deliver --session N --after-pid PID`.
// It must return immediately: the hook may not wait for delivery.
type Spawner interface {
	SpawnDeliver(sessionID int64, afterPID int) error
}

// SpawnerFunc adapts a function to Spawner.
type SpawnerFunc func(sessionID int64, afterPID int) error

// SpawnDeliver implements Spawner.
func (f SpawnerFunc) SpawnDeliver(sessionID int64, afterPID int) error { return f(sessionID, afterPID) }

// ExecSpawner runs Exe in a new session (Setsid) with stdin from /dev/null
// and stdout/stderr appended to LogPath, then releases it. The child never
// inherits the hook's stdout pipe, so Claude Code does not wait for it.
type ExecSpawner struct {
	Exe     string   // absolute agentctl path
	Env     []string // nil: inherit the hook's environment
	LogPath string   // <home>/logs/deliver.log
}

// SpawnDeliver implements Spawner.
func (s ExecSpawner) SpawnDeliver(sessionID int64, afterPID int) error {
	if !filepath.IsAbs(s.Exe) || !filepath.IsAbs(s.LogPath) {
		return errors.New("spawn deliver: agentctl path and log path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(s.LogPath), 0o700); err != nil {
		return fmt.Errorf("spawn deliver: %w", err)
	}
	logf, err := os.OpenFile(s.LogPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("spawn deliver: %w", err)
	}
	defer logf.Close()
	cmd := exec.Command(s.Exe, "deliver", "--session", strconv.FormatInt(sessionID, 10), "--after-pid", strconv.Itoa(afterPID))
	cmd.Env = s.Env
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn deliver: %w", err)
	}
	return cmd.Process.Release()
}
