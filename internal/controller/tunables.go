package controller

import "time"

// Tunables are agentctl's timing knobs. Tests shorten them via Config.Tunables;
// a zero field means its default.
type Tunables struct {
	AckWindow       time.Duration // sent -> unconfirmed when no UserPromptSubmit arrives
	DeliverSettle   time.Duration // deliver waits this long after the hook process exits
	PasteEnterDelay time.Duration // between paste-buffer and send-keys Enter
	StartingTimeout time.Duration // starting with no hook -> unknown / none
	Poll            time.Duration // TUI EventsSince poll interval
	Reconcile       time.Duration // TUI Reconcile + DrainAll interval
	HookBudget      time.Duration // agentctl hook self-timeout (hookingest keeps its own copy)
}

// DefaultTunables are the ARCHITECTURE.md values.
func DefaultTunables() Tunables {
	return Tunables{
		AckWindow:       15 * time.Second,
		DeliverSettle:   250 * time.Millisecond,
		PasteEnterDelay: 100 * time.Millisecond,
		StartingTimeout: 30 * time.Second,
		Poll:            300 * time.Millisecond,
		Reconcile:       2 * time.Second,
		HookBudget:      1800 * time.Millisecond,
	}
}

func (t Tunables) withDefaults() Tunables {
	d := DefaultTunables()
	for _, f := range []struct {
		v   *time.Duration
		def time.Duration
	}{
		{&t.AckWindow, d.AckWindow}, {&t.DeliverSettle, d.DeliverSettle}, {&t.PasteEnterDelay, d.PasteEnterDelay},
		{&t.StartingTimeout, d.StartingTimeout}, {&t.Poll, d.Poll}, {&t.Reconcile, d.Reconcile}, {&t.HookBudget, d.HookBudget},
	} {
		if *f.v <= 0 {
			*f.v = f.def
		}
	}
	return t
}
