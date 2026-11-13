package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Docking shows an agent's pane beside the agentctl dashboard pane, in the
// dashboard's own window, instead of switching the client to the agent's
// session. The agent pane is swapped (swap-pane) with a placeholder pane, so:
//
//   - the agent's session keeps a pane (the placeholder), so the session
//     lives on and a later swap puts the agent back where it was;
//   - the agent pane keeps its pane id and pid, so its identity still
//     verifies (invariant 5) and prompts still reach it.
//
// Pane user options record the state and travel with the panes, so any
// agentctl process can find and undo a dock from tmux alone.
const (
	// OptDockSlot is set on a docked agent pane: the id of the placeholder
	// pane holding its place in its own session.
	OptDockSlot = "@agentctl_dock_slot"
	// OptPlaceholder is "1" on a placeholder pane.
	OptPlaceholder = "@agentctl_placeholder"
)

// placeholderScript prints $1 and waits forever; tmux execs sh directly
// (multi-element command), so the text never passes through a shell parse.
const placeholderScript = `printf '%s\n' "$1"; exec tail -f /dev/null`

// DockSplitPercent is the share of the dashboard's window the docked agent
// pane gets.
const DockSplitPercent = "60%"

// WindowPane is one pane of a window, with agentctl's dock options.
type WindowPane struct {
	PaneID      string
	Placeholder bool   // OptPlaceholder is set
	DockSlot    string // OptDockSlot ("" when the pane is not docked)
}

const windowPaneFormat = "#{pane_id}\t#{" + OptPlaceholder + "}\t#{" + OptDockSlot + "}"

// WindowPanesCommand lists the panes of the window that contains target.
func WindowPanesCommand(target string) []string {
	return []string{"list-panes", "-t", target, "-F", windowPaneFormat}
}

// WindowPanes lists the panes in target's window.
func (c Client) WindowPanes(ctx context.Context, target string) ([]WindowPane, error) {
	if !isTmuxID(target, '%') {
		return nil, fmt.Errorf("tmux: invalid pane id %q", target)
	}
	res, err := c.run(ctx, nil, WindowPanesCommand(target))
	if err != nil {
		return nil, err
	}
	var out []WindowPane
	for _, line := range strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || !isTmuxID(f[0], '%') {
			continue
		}
		out = append(out, WindowPane{PaneID: f[0], Placeholder: f[1] == "1", DockSlot: validSlot(f[2])})
	}
	return out, nil
}

// DockSlot reads OptDockSlot of pane ("" when it is not docked).
func (c Client) DockSlot(ctx context.Context, pane string) (string, error) {
	if !isTmuxID(pane, '%') {
		return "", fmt.Errorf("tmux: invalid pane id %q", pane)
	}
	res, err := c.run(ctx, nil, []string{"display-message", "-p", "-t", pane, "#{" + OptDockSlot + "}"})
	if err != nil {
		return "", err
	}
	return validSlot(strings.TrimSpace(string(res.Stdout))), nil
}

func validSlot(s string) string {
	if isTmuxID(s, '%') {
		return s
	}
	return ""
}

// SplitPlaceholderCommand splits host's window, putting a placeholder pane
// that shows text to host's right, and prints the new pane id. -d keeps
// host active.
func SplitPlaceholderCommand(host, text string) []string {
	return []string{"split-window", "-h", "-d", "-l", DockSplitPercent, "-t", host, "-P", "-F", "#{pane_id}",
		"--", "sh", "-c", placeholderScript, "agentctl-placeholder", text}
}

// SplitPlaceholder creates a placeholder pane beside host and marks it with
// OptPlaceholder. It returns the new pane id.
func (c Client) SplitPlaceholder(ctx context.Context, host, text string) (string, error) {
	if !isTmuxID(host, '%') {
		return "", fmt.Errorf("tmux: invalid pane id %q", host)
	}
	if strings.IndexByte(text, 0) >= 0 {
		return "", errors.New("tmux: placeholder text contains NUL")
	}
	res, err := c.run(ctx, nil, SplitPlaceholderCommand(host, text))
	if err != nil {
		return "", err
	}
	pane := strings.TrimSpace(string(res.Stdout))
	if !isTmuxID(pane, '%') {
		return "", fmt.Errorf("tmux: unexpected split-window output %q", pane)
	}
	if _, err := c.run(ctx, nil, []string{"set-option", "-p", "-t", pane, OptPlaceholder, "1"}); err != nil {
		return "", err
	}
	return pane, nil
}

// UndockCommands send a docked pane home: it swaps places with slot (its
// placeholder, which lands where the pane was) and loses its dock options.
func UndockCommands(pane, slot string) [][]string {
	return [][]string{
		{"swap-pane", "-d", "-s", pane, "-t", slot},
		{"set-option", "-p", "-q", "-u", "-t", pane, OptDockSlot},
		{"set-option", "-p", "-q", "-u", "-t", pane, "remain-on-exit"},
	}
}

// DockInCommands bring agent into slot's place (slot, a placeholder in the
// dashboard's window, takes agent's place in its session), record slot on
// agent, keep agent's pane after its process exits (the dashboard's window
// does not have remain-on-exit), and focus it.
func DockInCommands(agent, slot string) [][]string {
	return [][]string{
		{"swap-pane", "-d", "-s", slot, "-t", agent},
		{"set-option", "-p", "-t", agent, OptDockSlot, slot},
		{"set-option", "-p", "-t", agent, "remain-on-exit", "on"},
		{"select-pane", "-t", agent},
	}
}

// Dock runs a dock plan (UndockCommands and DockInCommands, in order) as
// one tmux invocation, so no other client sees a half-done swap.
func (c Client) Dock(ctx context.Context, cmds [][]string) error {
	if len(cmds) == 0 {
		return nil
	}
	for _, cmd := range cmds {
		for _, a := range cmd {
			if strings.HasPrefix(a, "%") && !isTmuxID(a, '%') {
				return fmt.Errorf("tmux: invalid pane id %q", a)
			}
		}
	}
	_, err := c.run(ctx, nil, cmds...)
	return err
}

// SelectPane makes pane the active pane of its window.
func (c Client) SelectPane(ctx context.Context, pane string) error {
	if !isTmuxID(pane, '%') {
		return fmt.Errorf("tmux: invalid pane id %q", pane)
	}
	_, err := c.run(ctx, nil, []string{"select-pane", "-t", pane})
	return err
}
