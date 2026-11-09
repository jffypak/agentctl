package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jffypak/agentctl/internal/domain"
)

// slowLoad is how long a refresh can take on a loaded machine under -race:
// pollCmd spawns git (Overview -> MainRepo) and queries SQLite. It is longer
// than the 200ms after which the old harness silently dropped a command.
const slowLoad = 300 * time.Millisecond

// TestRunDeliversSlowCommands pins the root cause of the post-merge flake in
// TestRefreshFollowsTheSelectedTask: run() must deliver every command's
// message however long the command takes. It used to give up after 200ms
// (to skip ticks), so a refresh that was merely slow was dropped and the
// detail "did not follow the change".
func TestRunDeliversSlowCommands(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seed(t, everyState)
	d := press(t, f.model(t, 120, 40, Options{}), "j", "j", "j", "j", "j", "j", "j", "j", "enter") // payments
	sess, err := f.st.Sessions().LatestForTask(context.Background(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Sessions().Transition(context.Background(), sess.ID, domain.StateAwaitingInput, domain.ConfidenceHook, "permission prompt", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
	poll := pollCmd(d.sh, d.lastID, false)
	slow := func() tea.Msg { time.Sleep(slowLoad); return poll() } // a loaded machine, deterministically
	d = run(t, d, tea.Batch(slow, flashInfo("batched with the slow refresh")))
	if !strings.Contains(screenText(d), "payments  NEEDS YOU · awaiting input") {
		t.Fatalf("a slow refresh was dropped:\n%s", screenText(d))
	}
}
