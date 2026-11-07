package claude

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-hook-state.

// EnsureSettings makes sure <home>/hooks/claude-settings.json exists and is
// current, and returns its path. Start calls it before every launch.
//
// Contract for m1-hook-state: generate one command hook per mapped event
// whose command is the shell-quoted a.cfg.AgentctlPath + " hook", timeout 5;
// write atomically (temp file + rename) with mode 0600; a rewrite of
// unchanged content must be byte-identical. Keep this signature.
//
// The stub writes nothing and only returns the path.
func (a *Adapter) EnsureSettings(home string) (string, error) {
	return SettingsPath(home), nil
}
