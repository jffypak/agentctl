package tmux

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file (m2-prompt-queue) holds the only builders that write into a pane:
// load-buffer / paste-buffer / send-keys Enter (archtest: invariant 3). Only
// adapter/claude/deliver.go uses them. Like CaptureCommands, every write is
// guarded inside the server by the pane's identity, so a %N reused between the
// caller's own verification and the write never receives anything.

// BufferPrefix starts every buffer agentctl loads (agentctl-msg-<message id>).
const BufferPrefix = "agentctl-msg-"

// ErrPasteRefused: the server refused the paste or the Enter (identity
// changed, pane dead, pane in copy mode, or its input is off). Nothing was
// written to the pane by the refused command.
var ErrPasteRefused = errors.New("tmux: pane refused the write")

const (
	pasteOKMarker      = "agentctl-paste-ok"
	pasteRefusedMarker = "agentctl-paste-refused"
	enterOKMarker      = "agentctl-enter-ok"
	enterRefusedMarker = "agentctl-enter-refused"
)

var bufferNamePattern = regexp.MustCompile(`^` + BufferPrefix + `[0-9]+$`)

// BufferName is the buffer for message id.
func BufferName(messageID int64) string { return BufferPrefix + strconv.FormatInt(messageID, 10) }

// WriteTarget is a pane plus the identity it must still have when the server
// writes into it.
type WriteTarget struct {
	PaneID    string
	ServerPID int
	PanePID   int
}

func (t WriteTarget) validate() error {
	if !paneIDPattern.MatchString(t.PaneID) {
		return fmt.Errorf("tmux: invalid pane id %q", t.PaneID)
	}
	if t.ServerPID <= 0 || t.PanePID <= 0 {
		return fmt.Errorf("tmux: write to %s needs the server pid and pane pid it was bound to", t.PaneID)
	}
	return nil
}

// writeGuard is true only for this exact pane (server pid, pane id, pane pid),
// alive, not in a mode (copy mode would eat the Enter) and accepting input.
func (t WriteTarget) writeGuard() string {
	return fmt.Sprintf("#{&&:#{==:#{pid},%d},#{&&:#{==:#{pane_id},%s},#{&&:#{==:#{pane_pid},%d},"+
		"#{&&:#{==:#{pane_dead},0},#{&&:#{==:#{pane_in_mode},0},#{==:#{pane_input_off},0}}}}}}",
		t.ServerPID, t.PaneID, t.PanePID)
}

// refusal prints marker plus why the guard failed (for the delivery log).
// display-message tolerates a missing target (formats expand empty), and it
// runs its text through strftime, so the pane id is never compared here.
func (t WriteTarget) refusal(marker string) string {
	return fmt.Sprintf("display-message -p -t %s '%s #{?pane_id,#{?pane_in_mode,pane in copy mode,#{?pane_input_off,pane input off,#{?pane_dead,pane dead,identity changed}}},pane missing}'",
		t.PaneID, marker)
}

// LoadPasteCommands loads stdin into buffer and pastes it into the pane in
// one invocation:
//
//	load-buffer -b agentctl-msg-N - ;
//	if-shell -F -t %P '<guard>' 'display-message -p ok ; paste-buffer -p -d -b B -t %P' \
//	  'display-message -p refused… ; delete-buffer -b B'
//
// -p wraps the text in bracketed-paste codes (the agent asked for them), so
// line feeds inside it never submit; -d deletes the buffer after pasting, and
// the refused branch deletes it too: no message text stays in tmux.
func LoadPasteCommands(t WriteTarget, buffer string) ([][]string, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	if !bufferNamePattern.MatchString(buffer) {
		return nil, fmt.Errorf("tmux: invalid buffer name %q", buffer)
	}
	paste := fmt.Sprintf("display-message -p %s ; paste-buffer -p -d -b %s -t %s", pasteOKMarker, buffer, t.PaneID)
	refused := t.refusal(pasteRefusedMarker) + " ; delete-buffer -b " + buffer
	return [][]string{
		{"load-buffer", "-b", buffer, "-"},
		{"if-shell", "-F", "-t", t.PaneID, t.writeGuard(), paste, refused},
	}, nil
}

// SendEnterCommand presses Enter in the pane, under the same guard.
func SendEnterCommand(t WriteTarget) ([]string, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	enter := fmt.Sprintf("display-message -p %s ; send-keys -t %s Enter", enterOKMarker, t.PaneID)
	return []string{"if-shell", "-F", "-t", t.PaneID, t.writeGuard(), enter, t.refusal(enterRefusedMarker)}, nil
}

// ListBuffersCommand lists every paste buffer's name.
func ListBuffersCommand() []string { return []string{"list-buffers", "-F", "#{buffer_name}"} }

// DeleteBufferCommand deletes one agentctl buffer.
func DeleteBufferCommand(buffer string) ([]string, error) {
	if !bufferNamePattern.MatchString(buffer) {
		return nil, fmt.Errorf("tmux: invalid buffer name %q", buffer)
	}
	return []string{"delete-buffer", "-b", buffer}, nil
}

// LoadAndPaste loads body into buffer and pastes it into the verified pane.
// ErrPasteRefused (nothing pasted, buffer deleted) when the guard fails.
// Any other error leaves it unknown whether the paste happened.
func (c Client) LoadAndPaste(ctx context.Context, t WriteTarget, buffer, body string) error {
	cmds, err := LoadPasteCommands(t, buffer)
	if err != nil {
		return err
	}
	res, err := c.run(ctx, strings.NewReader(body), cmds...)
	if err != nil {
		return err
	}
	return verdict(res.Stdout, pasteOKMarker, pasteRefusedMarker, "paste")
}

// SendEnter presses Enter in the verified pane. ErrPasteRefused when the
// guard fails (no key was sent).
func (c Client) SendEnter(ctx context.Context, t WriteTarget) error {
	cmd, err := SendEnterCommand(t)
	if err != nil {
		return err
	}
	res, err := c.run(ctx, nil, cmd)
	if err != nil {
		return err
	}
	return verdict(res.Stdout, enterOKMarker, enterRefusedMarker, "Enter")
}

func verdict(out []byte, ok, refused, what string) error {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	switch {
	case line == ok:
		return nil
	case strings.HasPrefix(line, refused):
		return fmt.Errorf("%w: %s refused (%s)", ErrPasteRefused, what, strings.TrimSpace(strings.TrimPrefix(line, refused)))
	}
	return fmt.Errorf("tmux: unexpected %s output %q", what, line)
}

// ListBuffers returns every paste buffer name on the server.
func (c Client) ListBuffers(ctx context.Context) ([]string, error) {
	res, err := c.run(ctx, nil, ListBuffersCommand())
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// DeleteBuffer removes an agentctl buffer (cleanup after a failed paste).
func (c Client) DeleteBuffer(ctx context.Context, buffer string) error {
	cmd, err := DeleteBufferCommand(buffer)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, nil, cmd)
	return err
}
