package tui

import (
	"fmt"

	"charm.land/bubbles/v2/key"
)

// Action names for every Phase-1 key. They are stable: config.yaml `keys:`
// (m2-tui-manage, keys_config.go) refers to them.
const (
	ActUp            = "up"             // list: previous task
	ActDown          = "down"           // list: next task
	ActOpen          = "open"           // list: open detail; detail: attach/focus the pane
	ActBack          = "back"           // close a view / leave detail / clear the filter; quits from the bare list
	ActQuit          = "quit"           // leave detail, else quit
	ActForceQuit     = "force_quit"     // quit from anywhere (ctrl+c)
	ActFilter        = "filter"         // start typing a filter
	ActPrompt        = "prompt"         // compose a prompt (m2-prompt-queue)
	ActDiff          = "diff"           // diff view (m2-tui-manage)
	ActLogs          = "logs"           // recent output (m2-tui-manage)
	ActNew           = "new"            // new task form (m2-tui-manage)
	ActAdopt         = "adopt"          // adopt picker (m2-tui-manage)
	ActClose         = "close"          // close confirmation (m2-tui-manage)
	ActCancelMessage = "cancel_message" // detail: cancel the selected queued message (m2-prompt-queue)
	ActSend          = "send"           // composer: send (m2-prompt-queue)
	ActFullCapture   = "full_capture"   // logs view: write + page the full capture (m2-tui-manage)
	ActConfirm       = "confirm"        // confirm dialogs: yes (the default is always No)
	ActNextTab       = "next_tab"       // list: next tab (All, Needs you, Unread, Running, Idle)
	ActPrevTab       = "prev_tab"       // list: previous tab
)

// Actions lists every action name in a stable order.
func Actions() []string {
	return []string{ActUp, ActDown, ActOpen, ActBack, ActQuit, ActForceQuit, ActFilter, ActPrompt, ActDiff,
		ActLogs, ActNew, ActAdopt, ActClose, ActCancelMessage, ActSend, ActFullCapture, ActConfirm,
		ActNextTab, ActPrevTab}
}

// KeyMap holds the binding for every action. Bindings are matched with
// key.Matches against tea.KeyPressMsg; their help text feeds the footer.
// Context decides which bindings are live: q and n mean different things in
// a text field than in the list, so text inputs see keys before the keymap.
type KeyMap struct {
	Up, Down, Open, Back, Quit, ForceQuit, Filter key.Binding
	Prompt, Diff, Logs, New, Adopt, Close         key.Binding
	CancelMessage, Send, FullCapture, Confirm     key.Binding
	NextTab, PrevTab                              key.Binding
}

// DefaultKeyMap is spec §4.5 plus the Phase-1 extras (adopt, close, cancel,
// send, full capture, confirm).
func DefaultKeyMap() KeyMap {
	b := func(help, desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
	}
	return KeyMap{
		Up:            b("↑/k", "move", "k", "up"),
		Down:          b("↓/j", "move", "j", "down"),
		Open:          b("↵", "open", "enter"),
		Back:          b("esc", "back", "esc"),
		Quit:          b("q", "quit", "q"),
		ForceQuit:     b("ctrl+c", "quit", "ctrl+c"),
		Filter:        b("/", "filter", "/"),
		Prompt:        b("p", "prompt", "p"),
		Diff:          b("d", "diff", "d"),
		Logs:          b("l", "logs", "l"),
		New:           b("n", "new", "n"),
		Adopt:         b("a", "adopt", "a"),
		Close:         b("x", "close", "x"),
		CancelMessage: b("c", "cancel message", "c"),
		Send:          b("ctrl+s", "send", "ctrl+s"),
		FullCapture:   b("f", "full capture", "f"),
		Confirm:       b("y", "yes", "y"),
		NextTab:       b("tab", "next tab", "tab"),
		PrevTab:       b("⇧tab", "previous tab", "shift+tab"),
	}
}

// field returns a pointer to the binding for action (nil if unknown).
func (k *KeyMap) field(action string) *key.Binding {
	switch action {
	case ActUp:
		return &k.Up
	case ActDown:
		return &k.Down
	case ActOpen:
		return &k.Open
	case ActBack:
		return &k.Back
	case ActQuit:
		return &k.Quit
	case ActForceQuit:
		return &k.ForceQuit
	case ActFilter:
		return &k.Filter
	case ActPrompt:
		return &k.Prompt
	case ActDiff:
		return &k.Diff
	case ActLogs:
		return &k.Logs
	case ActNew:
		return &k.New
	case ActAdopt:
		return &k.Adopt
	case ActClose:
		return &k.Close
	case ActCancelMessage:
		return &k.CancelMessage
	case ActSend:
		return &k.Send
	case ActFullCapture:
		return &k.FullCapture
	case ActConfirm:
		return &k.Confirm
	case ActNextTab:
		return &k.NextTab
	case ActPrevTab:
		return &k.PrevTab
	}
	return nil
}

// Binding returns the binding for action.
func (k KeyMap) Binding(action string) (key.Binding, bool) {
	if b := k.field(action); b != nil {
		return *b, true
	}
	return key.Binding{}, false
}

// WithKeys returns a copy of k with action bound to keys (help text keeps its
// description; the key column shows the new keys). k itself is unchanged.
func (k KeyMap) WithKeys(action string, keys ...string) (KeyMap, error) {
	out := k
	b := out.field(action)
	if b == nil {
		return k, fmt.Errorf("unknown key action %q", action)
	}
	if len(keys) == 0 {
		return k, fmt.Errorf("key action %q needs at least one key", action)
	}
	help := keys[0]
	for _, s := range keys[1:] {
		help += "/" + s
	}
	*b = key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, b.Help().Desc))
	return out, nil
}
