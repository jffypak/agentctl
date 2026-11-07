package tmux

import (
	"syscall"
	"testing"
)

// tmux 3.5a prints pane_dead_signal as a lowercase name ("term") with an empty
// pane_dead_status for a pane killed by a signal. One such pane must not make
// the whole server snapshot unparseable.
func TestParsePaneDeadBySignalName(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		signal     string
		wantStatus int
		wantSignal int
	}{
		{"alive", "", "", 0, 0},
		{"clean exit", "0", "", 0, 0},
		{"exit 3", "3", "", 3, 0},
		{"numeric signal", "", "15", 0, 15},
		{"term by name", "", "term", 0, int(syscall.SIGTERM)},
		{"int by name", "", "int", 0, int(syscall.SIGINT)},
		{"kill by name", "", "kill", 0, int(syscall.SIGKILL)},
		{"segv upper-case SIG prefix", "", "SIGSEGV", 0, int(syscall.SIGSEGV)},
		{"bus is platform-correct", "", "bus", 0, int(syscall.SIGBUS)},
		{"unknown name is never zero", "", "wat", 0, UnknownSignal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dead := "1"
			if tc.status == "" && tc.signal == "" {
				dead = "0"
			}
			line := "4242\t$1\t0\t%3\t5151\t" + dead + "\t" + tc.status + "\t" + tc.signal + "\tagentctl-x\tclaude\t/tmp/wt"
			p, err := parsePane(line)
			if err != nil {
				t.Fatalf("parsePane(%q): %v", line, err)
			}
			if p.DeadStatus != tc.wantStatus || p.DeadSignal != tc.wantSignal {
				t.Fatalf("status/signal = %d/%d, want %d/%d", p.DeadStatus, p.DeadSignal, tc.wantStatus, tc.wantSignal)
			}
		})
	}
}

func TestParsePaneStillRejectsGarbageStatus(t *testing.T) {
	line := "4242\t$1\t0\t%3\t5151\t1\tnope\t\tagentctl-x\tclaude\t/tmp/wt"
	if _, err := parsePane(line); err == nil {
		t.Fatal("want an error for a non-numeric pane_dead_status")
	}
}
