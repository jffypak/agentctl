package main

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m2-prompt-queue.
//
// `agentctl send <name> <message>` / `send --cancel <id>`: validate, queue, Drain.
func cmdSend(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "send", "m2-prompt-queue")
}
