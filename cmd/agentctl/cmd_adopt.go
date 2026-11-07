package main

import "context"

var _ = registerStub("adopt", "m1-pane-identity")

// STUB (m1-walking-skeleton) — owned and rewritten whole by m1-pane-identity.
//
// `agentctl adopt` lists unregistered candidate panes; `agentctl adopt %N --name <name>`
// registers one (origin adopted, state unknown/none, events task.created + session.adopted).
func cmdAdopt(ctx context.Context, sys *system, args []string) int {
	return notImplemented(sys, "adopt", "m1-pane-identity")
}
