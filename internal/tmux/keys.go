package tmux

import (
	"context"
	"strings"
)

// ReturnKey is the prefix-table key bound to plain `switch-client -l` (back
// to the last session), "" if none. tmux binds L by default; user configs
// often rebind it.
func (c Client) ReturnKey(ctx context.Context) (string, error) {
	res, err := c.run(ctx, nil, []string{"list-keys", "-T", "prefix"})
	if err != nil {
		return "", err
	}
	return ReturnKeyFrom(string(res.Stdout)), nil
}

// ReturnKeyFrom parses `list-keys -T prefix` output: lines are
// `bind-key [-r] -T prefix <key> <command...>`. The first key whose command
// is exactly `switch-client -l` wins.
func ReturnKeyFrom(out string) string {
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "bind-key" {
			continue
		}
		i := 1
		for i < len(f) && strings.HasPrefix(f[i], "-") {
			if f[i] == "-T" || f[i] == "-N" {
				i++ // the flag takes a value
			}
			i++
		}
		if i+1 < len(f) && strings.Join(f[i+1:], " ") == "switch-client -l" {
			return f[i]
		}
	}
	return ""
}
