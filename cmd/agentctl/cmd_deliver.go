package main

import "context"

// STUB (m1-walking-skeleton) — owned and rewritten whole by m2-prompt-queue.
//
// `agentctl deliver --session <id> --after-pid <pid>`: wait for the hook pid, DeliverSettle,
// controller.Drain, wait up to AckWindow; always exit 0; log to logs/deliver.log.
func cmdDeliver(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "deliver", "m2-prompt-queue")
}
