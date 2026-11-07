package claude

import "github.com/jffypak/agentctl/internal/adapter"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-hook-state.

// ParseHook maps one Claude Code hook payload (stdin JSON) plus the inherited
// environment to an observation, following the hook->state table in
// ARCHITECTURE.md §Agent adapter. It must be pure (no I/O, never opens
// transcript_path) and must keep only a sha256 of any prompt text.
func (a *Adapter) ParseHook(payload []byte, env adapter.HookEnv) (adapter.HookObservation, error) {
	return adapter.HookObservation{}, adapter.ErrNotImplemented
}
