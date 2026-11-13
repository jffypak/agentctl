package hookingest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jffypak/agentctl/internal/adapter"
	"github.com/jffypak/agentctl/internal/store"
)

// Orphan reasons added by codex-support.
const (
	// OrphanCodexDaemon: a Codex hook ran inside the shared `codex
	// app-server` daemon. Its $TMUX/$TMUX_PANE are the daemon's (the pane it
	// was first started from), not the agent's, so it can never be matched.
	OrphanCodexDaemon = "codex_daemon"
	// OrphanAgentMismatch: the hook's agent (`hook --agent`) is not the
	// matched session's agent type.
	OrphanAgentMismatch = "agent_mismatch"
)

// parentLookupTimeout bounds the ps call of the daemon guard.
const parentLookupTimeout = 300 * time.Millisecond

// isCodexDaemon reports whether a process command line is Codex's shared
// app-server (`…/codex app-server …`, including its `daemon` supervisor).
// Embedded Codex runs as plain `codex …` in the pane.
func isCodexDaemon(cmdline string) bool {
	f := strings.Fields(cmdline)
	return len(f) >= 2 && filepath.Base(f[0]) == "codex" && f[1] == "app-server"
}

// psParentCommand returns the command lines of this process's parent and
// grandparent, one per line. Codex runs a hook command through `sh -c`, which
// may or may not exec it, so the agent is the parent or the grandparent.
func psParentCommand() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), parentLookupTimeout)
	defer cancel()
	var lines []string
	pid := os.Getppid()
	for i := 0; i < 2 && pid > 1; i++ {
		out, err := exec.CommandContext(ctx, "ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return strings.Join(lines, "\n"), fmt.Errorf("read parent process %d: %w", pid, err)
		}
		f := strings.Fields(string(out))
		if len(f) == 0 {
			break
		}
		lines = append(lines, strings.Join(f[1:], " "))
		pid, _ = strconv.Atoi(f[0])
	}
	return strings.Join(lines, "\n"), nil
}

// fromCodexDaemon reports whether any line of a parent chain is the daemon.
func fromCodexDaemon(chain string) bool {
	for _, l := range strings.Split(chain, "\n") {
		if isCodexDaemon(l) {
			return true
		}
	}
	return false
}

// guardForeignSession keeps a Codex hook from another Codex session in the
// same pane from moving the session's state (e.g. a helper thread). Once a
// session has a Codex session_id, a hook with a different one is recorded
// but changes nothing — unless it is SessionStart with source clear, resume
// or fork, which legitimately starts a new Codex session in the same TUI.
func guardForeignSession(sess store.Session, obs adapter.HookObservation) adapter.HookObservation {
	if sess.AgentSessionID == "" || obs.AgentSessionID == "" || obs.AgentSessionID == sess.AgentSessionID {
		return obs
	}
	if obs.Event == "SessionStart" {
		switch obs.Attrs["source"] {
		case "clear", "resume", "fork":
			return obs
		}
	}
	out := obs
	out.State, out.Detail, out.AgentSessionID, out.PromptSHA256 = "", "", "", ""
	out.Prompt, out.LastMessage, out.Model, out.TurnEnded = "", "", "", false // another session's text
	out.Attrs = make(map[string]string, len(obs.Attrs)+1)
	for k, v := range obs.Attrs {
		out.Attrs[k] = v
	}
	out.Attrs["foreign_session"] = "1"
	return out
}
