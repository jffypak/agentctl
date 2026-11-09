package tui

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/jffypak/agentctl/internal/config"
)

// Keys from config.yaml (spec §4.5: "key bindings must be configurable").
//
//	keys:
//	  diff: D
//	  logs: [l, L]
//
// Action names are the Act* constants (keys.go). A config is applied whole or
// not at all: any unknown action, empty or blank binding, key bound to two
// actions, or confirm bound to a key that means No (enter, esc) leaves every
// default in place and produces one warning line for the banner. Keys are
// globally unique, so no context can turn a familiar key into a different,
// possibly destructive, action.

// confirmForbidden are keys that answer No in a y/N confirmation; binding
// confirm to them would make the default answer Yes.
var confirmForbidden = []string{"enter", "esc"}

// ConfigKeyMap reads config.yaml (located from getenv, see config.Path) and
// returns the keymap to run with — nil means the defaults — and warning with
// any config problem appended ("; "-joined, one line). A missing file is
// silent; a malformed one is a banner and the defaults. cmd_tui calls it once
// at startup.
func ConfigKeyMap(getenv func(string) string, warning string) (*KeyMap, string) {
	path := config.Path(getenv)
	cfg, err := config.Load(path)
	if err != nil {
		msg := strings.TrimPrefix(oneLine(err.Error()), config.ErrMalformed.Error()+": ")
		if !errors.Is(err, config.ErrMalformed) {
			msg = oneLine(err.Error())
		}
		return nil, joinWarnings(warning, "config.yaml: "+msg+"; using default keys ("+path+")")
	}
	if len(cfg.Keys) == 0 {
		return nil, warning
	}
	km, err := KeyMapFromConfig(cfg.Keys)
	if err != nil {
		return nil, joinWarnings(warning, err.Error()+"; using default keys ("+path+")")
	}
	return &km, warning
}

// KeyMapFromConfig applies keys (action -> keys) over DefaultKeyMap. On any
// problem it returns the defaults and one error line naming every problem.
func KeyMapFromConfig(keys map[string][]string) (KeyMap, error) {
	def := DefaultKeyMap()
	km := def
	var problems []string
	known := Actions()
	names := make([]string, 0, len(keys))
	for a := range keys {
		names = append(names, a)
	}
	slices.Sort(names)
	for _, a := range names {
		if !slices.Contains(known, a) {
			problems = append(problems, "unknown action "+strconv.Quote(a))
		}
	}
	for _, a := range known {
		list, ok := keys[a]
		if !ok {
			continue
		}
		clean, msg := cleanKeys(a, list)
		if msg != "" {
			problems = append(problems, msg)
			continue
		}
		next, err := km.WithKeys(a, clean...)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		km = next
	}
	problems = append(problems, conflicts(km)...)
	for _, k := range km.Confirm.Keys() {
		if slices.Contains(confirmForbidden, k) {
			problems = append(problems, "confirm cannot use "+strconv.Quote(k)+" (it means No in a y/N question)")
		}
	}
	if len(problems) > 0 {
		return def, errors.New("config.yaml keys: " + strings.Join(problems, "; "))
	}
	return km, nil
}

// cleanKeys validates one binding and drops duplicates.
func cleanKeys(action string, list []string) ([]string, string) {
	if len(list) == 0 {
		return nil, action + " has no keys"
	}
	var out []string
	for _, k := range list {
		if k == "" || strings.ContainsFunc(k, unicode.IsSpace) {
			return nil, "invalid key " + strconv.Quote(k) + " for " + action + " (use names like d, D, ctrl+s, enter, space)"
		}
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out, ""
}

// conflicts names every key bound to more than one action.
func conflicts(km KeyMap) []string {
	owner := map[string]string{}
	var out []string
	for _, a := range Actions() {
		b, _ := km.Binding(a)
		for _, k := range b.Keys() {
			if prev, taken := owner[k]; taken {
				out = append(out, strconv.Quote(k)+" is bound to both "+prev+" and "+a)
				continue
			}
			owner[k] = a
		}
	}
	return out
}

func joinWarnings(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
