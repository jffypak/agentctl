// Package paths resolves agentctl's home (state) and config locations.
//
//	home   = $AGENTCTL_HOME, else $XDG_STATE_HOME/agentctl, else ~/.local/state/agentctl
//	         ├── agentctl.db
//	         ├── logs/
//	         └── hooks/claude-settings.json
//	config = $XDG_CONFIG_HOME/agentctl/config.yaml, else ~/.config/agentctl/config.yaml
//
// All results are absolute, because home is exported into agent sessions
// (AGENTCTL_HOME) whose cwd differs from ours.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnvHome overrides the home directory.
const EnvHome = "AGENTCTL_HOME"

// Paths is the resolved layout.
type Paths struct {
	Home           string
	DB             string
	Logs           string
	Hooks          string
	ClaudeSettings string
	ConfigFile     string
}

// ForHome lays out a known home directory (config left empty).
func ForHome(home string) Paths {
	return Paths{
		Home:           home,
		DB:             filepath.Join(home, "agentctl.db"),
		Logs:           filepath.Join(home, "logs"),
		Hooks:          filepath.Join(home, "hooks"),
		ClaudeSettings: filepath.Join(home, "hooks", "claude-settings.json"),
	}
}

// Resolve computes the layout from an environment lookup.
func Resolve(getenv func(string) string) (Paths, error) {
	home := getenv(EnvHome)
	userHome := getenv("HOME")
	if home == "" {
		if x := getenv("XDG_STATE_HOME"); filepath.IsAbs(x) {
			home = filepath.Join(x, "agentctl")
		} else if userHome != "" {
			home = filepath.Join(userHome, ".local", "state", "agentctl")
		} else {
			return Paths{}, errors.New("cannot locate agentctl home: set AGENTCTL_HOME or HOME")
		}
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve agentctl home %q: %w", home, err)
	}
	p := ForHome(abs)
	if x := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
		p.ConfigFile = filepath.Join(x, "agentctl", "config.yaml")
	} else if userHome != "" {
		p.ConfigFile = filepath.Join(userHome, ".config", "agentctl", "config.yaml")
	}
	return p, nil
}

// EnsureHome creates home, logs/ and hooks/ with mode 0700.
func EnsureHome(p Paths) error {
	for _, d := range []string{p.Home, p.Logs, p.Hooks} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}
