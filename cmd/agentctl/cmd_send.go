package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

const sendUsage = "usage: agentctl send <name> <message…> (message - reads stdin) | agentctl send --cancel <id>"

// cmdSend: `agentctl send <name> <message…>` validates the prompt, refuses
// agents that exited / failed / are disconnected (nothing stored), queues it
// and drains once: a ready agent gets it now (`#12 sent · awaiting ack`),
// otherwise it waits with the reason (`#12 queued · agent working`).
// `send --cancel <id>` cancels a queued message (only queued).
func cmdSend(ctx context.Context, sys *system, args []string) int {
	fs := newFlagSet(sys, "send")
	cancel := fs.Int64("cancel", 0, "cancel queued message `id`")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return flagExit(err)
	}
	switch {
	case *cancel != 0 && len(pos) != 0, *cancel < 0:
		return sys.usageError("send", "%s", sendUsage)
	case *cancel == 0 && len(pos) < 2:
		return sys.usageError("send", "%s", sendUsage)
	}
	var body string
	if *cancel == 0 {
		if body, err = sendBody(sys, pos[1:]); err != nil {
			return sys.fail(err)
		}
	}
	ctl, err := sys.openController(ctx)
	if err != nil {
		return sys.fail(err)
	}
	defer ctl.Close()
	if *cancel != 0 {
		v, err := ctl.CancelMessage(ctx, *cancel)
		if err != nil {
			return sys.fail(err)
		}
		fmt.Fprintf(sys.stdout, "#%d %s\n", v.ID, v.Label)
		return exitOK
	}
	res, err := ctl.Send(ctx, controller.SendRequest{TaskID: pos[0], Body: body, Source: controller.SendSourceCLI})
	if err != nil {
		return sys.fail(err)
	}
	fmt.Fprintf(sys.stdout, "#%d %s\n", res.Message.ID, res.Message.Label)
	if res.DrainErr != "" {
		fmt.Fprintf(sys.stderr, "agentctl send: delivery attempt failed (message kept as %s): %s\n", res.Message.State, res.DrainErr)
	}
	return exitOK
}

// sendBody is the message: the remaining arguments joined by spaces, or
// stdin for a single "-".
func sendBody(sys *system, words []string) (string, error) {
	if len(words) == 1 && words[0] == "-" {
		b, err := io.ReadAll(io.LimitReader(sys.stdin, domain.MaxMessageBytes+1))
		if err != nil {
			return "", fmt.Errorf("read message from stdin: %w", err)
		}
		return string(b), nil
	}
	return strings.Join(words, " "), nil
}
