// Package config reads agentctl's optional user configuration,
// $XDG_CONFIG_HOME/agentctl/config.yaml (else ~/.config/agentctl/config.yaml).
// Phase 1 knows exactly one section, the TUI key bindings:
//
//	keys:
//	  diff: D          # one key
//	  logs: [l, L]     # or several
//
// The package only parses: which action names exist and which bindings
// conflict is decided by the TUI (internal/tui keys_config.go), which owns
// the action names. A missing file is not an error; anything unreadable or
// malformed is, and the caller falls back to its defaults.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/jffypak/agentctl/internal/paths"
)

// ErrMalformed marks a config file that exists and was read but is not a
// valid Phase-1 config.yaml (YAML syntax, unknown settings, wrong shapes).
var ErrMalformed = errors.New("malformed config")

// Config is the parsed config.yaml.
type Config struct {
	// Keys maps an action name to the keys bound to it, in file order. An
	// action listed with no keys (`diff:` or `diff: []`) maps to an empty
	// slice so the caller can reject it.
	Keys map[string][]string
}

// file is the on-disk shape; unknown top-level settings are rejected.
type file struct {
	Keys map[string]keyList `yaml:"keys"`
}

// keyList is one action's binding: a scalar or a list of scalars. Values are
// taken verbatim (y stays "y", not a YAML 1.1 bool).
type keyList []string

// UnmarshalYAML accepts `d`, `[d, D]`, a block list, or null (no keys).
func (k *keyList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*k = keyList{}
			return nil
		}
		*k = keyList{n.Value}
		return nil
	case yaml.SequenceNode:
		out := make(keyList, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: a key must be a plain value like d or ctrl+s", c.Line)
			}
			out = append(out, c.Value)
		}
		*k = out
		return nil
	}
	return fmt.Errorf("line %d: a binding must be a key or a list of keys", n.Line)
}

// Path is the config file location for an environment lookup, or "" when
// neither XDG_CONFIG_HOME (absolute) nor HOME is set.
func Path(getenv func(string) string) string {
	p, err := paths.Resolve(getenv)
	if err != nil {
		return ""
	}
	return p.ConfigFile
}

// Load reads and parses path. An empty path or a missing file is the zero
// Config and no error.
func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse parses config.yaml content. Errors are one line and wrap ErrMalformed.
func Parse(data []byte) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var f file
	if err := dec.Decode(&f); err != nil {
		if errors.Is(err, io.EOF) { // empty or comments only
			return Config{}, nil
		}
		return Config{}, malformed(err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, malformed(errors.New("only one YAML document is allowed"))
	}
	if len(f.Keys) == 0 {
		return Config{}, nil
	}
	keys := make(map[string][]string, len(f.Keys))
	for action, list := range f.Keys {
		keys[action] = []string(list)
	}
	return Config{Keys: keys}, nil
}

func malformed(err error) error {
	msg := strings.TrimPrefix(oneLine(err.Error()), "yaml: ")
	msg = strings.TrimPrefix(msg, "unmarshal errors: ")
	return fmt.Errorf("%w: %s", ErrMalformed, msg)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
