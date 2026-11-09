package tui

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/jffypak/agentctl/internal/controller"
	"github.com/jffypak/agentctl/internal/domain"
)

// Canned controller answers: the goldens pin rendering, the behavior tests
// (manage_test.go) drive the real controller.
var (
	goldenDiff = controller.DiffSummary{
		Base: "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b", BaseRef: "main", Head: "9f8e7d6c5b4a39281706f5e4d3c2b1a098765432", Commits: 2,
		Total: []controller.FileChange{
			{Path: "internal/auth/login.go", Added: 42, Deleted: 7},
			{Path: "internal/auth/login_test.go", Added: 120},
			{Path: "internal/auth/session.go", OldPath: "internal/auth/sess.go", Added: 3, Deleted: 3},
			{Path: "assets/logo.png", Binary: true},
			{Path: "go.mod", Added: 1, Deleted: 1},
		},
		Untracked: []controller.FileChange{{Path: "notes/redirect-cases.md", Added: 12}},
		Conflicts: []string{"go.mod"},
	}
	goldenOutput = []string{
		"● Bash(npm test -- auth)",
		"  ⎿  PASS tests/auth/login.test.ts",
		"     FAIL tests/auth/redirect.test.ts",
		"       expected HTTP 302, received 200",
		"       at tests/auth/redirect.test.ts:88",
		"",
		"● I'll look at the redirect handler next.",
		"● Read(src/auth/redirect.ts)",
		"  ⎿  Read 64 lines",
		"● Update(src/auth/redirect.ts)",
		"",
		"Do you want to make this edit to redirect.ts?",
		"❯ 1. Yes",
		"  2. Yes, and don't ask again this session",
		"  3. No, and tell Claude what to do differently (esc)",
	}
	goldenCandidates = controller.AdoptCandidates{
		Socket: "/private/tmp/tmux-501/default", Commands: []string{"claude"},
		Panes: []controller.AdoptCandidate{
			{PaneID: "%3", PanePID: 51234, SessionID: "$2", SessionName: "work", WindowIndex: 1, Command: "claude", Cwd: "/Users/me/src/api"},
			{PaneID: "%7", PanePID: 51900, SessionID: "$4", SessionName: "scratch", WindowIndex: 0, Command: "claude", Cwd: "/Users/me/src/very/deeply/nested/project/with/a/long/path/web-frontend"},
			{PaneID: "%12", PanePID: 52011, SessionID: "$5", SessionName: "infra-oncall", WindowIndex: 3, Command: "claude", Cwd: "/tmp/scratch"},
		},
	}
)

func nonEmpty(lines []string) []string {
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestManageGolden pins the manage views at 80x24 and 120x40 (fixed clock,
// fixed palette, dark background). Update with:
// go test ./internal/tui -run TestManageGolden -update
func TestManageGolden(t *testing.T) {
	t.Parallel()
	seeded := func(t *testing.T, w, h int) Model {
		f := newFixture(t)
		f.seed(t, everyState)
		return f.model(t, w, h, Options{})
	}
	authTests := []string{"j", "j", "j"} // NEEDS YOU: legacy-pane, flaky-ci, migrate-db, auth-tests
	screens := []struct {
		name  string
		build func(t *testing.T, w, h int) Model
	}{
		{"diff", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), append(authTests, "d")...)
			return update(t, m, diffLoadedMsg{id: topView(m).(diffView).id, sum: goldenDiff})
		}},
		{"diff_error", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), "j", "d") // flaky-ci
			return update(t, m, diffLoadedMsg{id: topView(m).(diffView).id,
				err: &controller.Error{Kind: controller.ErrDiffUnavailable, Msg: "worktree /src/repo-worktrees/flaky-ci of task flaky-ci is missing"}})
		}},
		{"logs", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), append(authTests, "l")...)
			in := controller.Inspection{Pane: controller.PaneStatus{Available: true, Note: "alive"}, Output: nonEmpty(goldenOutput)}
			return update(t, m, logsLoadedMsg{id: topView(m).(logsView).id, in: in})
		}},
		{"logs_unavailable", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), "j", "l") // flaky-ci
			in := controller.Inspection{Pane: controller.PaneStatus{Note: "pane unavailable (disconnected: identity_mismatch)",
				Liveness: domain.Liveness{Status: domain.LivenessDisconnected, Reason: domain.ReasonIdentityMismatch}}}
			return update(t, m, logsLoadedMsg{id: topView(m).(logsView).id, in: in})
		}},
		{"new_error", func(t *testing.T, w, h int) Model {
			m := typeText(t, press(t, seeded(t, w, h), "n"), "auth-flow")
			m = typeText(t, press(t, m, "enter"), "Write integration tests for the OAuth login flow")
			m = press(t, m, "enter") // submitted; the controller answers:
			return update(t, m, newTaskDoneMsg{id: topView(m).(newTaskView).id, err: &controller.Error{Kind: controller.ErrAgentMissing,
				Msg: `agent command "claude" not found on PATH (install Claude Code or set AGENTCTL_AGENT_CMD)`}})
		}},
		{"new_invalid", func(t *testing.T, w, h int) Model {
			return typeText(t, press(t, seeded(t, w, h), "n"), "Auth Flow")
		}},
		{"adopt", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), "a")
			v := topView(m).(adoptView)
			m = update(t, m, adoptListedMsg{id: v.id, seq: v.listSeq, cands: goldenCandidates})
			return press(t, m, "j")
		}},
		{"adopt_name", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), "a")
			v := topView(m).(adoptView)
			m = update(t, m, adoptListedMsg{id: v.id, seq: v.listSeq, cands: goldenCandidates})
			return press(t, m, "enter")
		}},
		{"confirm", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), append(authTests, "x")...)
			return update(t, m, closePlanMsg{id: topView(m).(confirmView).id, stop: goldenStopPlan})
		}},
		{"confirm_stop", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), append(authTests, "x")...)
			m = update(t, m, closePlanMsg{id: topView(m).(confirmView).id, stop: goldenStopPlan})
			return press(t, m, "x")
		}},
		{"detail_inspected", func(t *testing.T, w, h int) Model {
			m := press(t, seeded(t, w, h), append(authTests, "enter")...)
			in := controller.Inspection{Pane: controller.PaneStatus{Available: true, Note: "alive"}, Output: nonEmpty(goldenOutput), Diff: goldenDiff}
			return update(t, m, inspectedMsg{taskID: "auth-tests", seq: 1, in: in})
		}},
	}
	for _, s := range screens {
		for _, size := range [][2]int{{80, 24}, {120, 40}} {
			t.Run(fmt.Sprintf("%s_%dx%d", s.name, size[0], size[1]), func(t *testing.T) {
				t.Parallel()
				m := s.build(t, size[0], size[1])
				teatest.RequireEqualOutput(t, []byte(m.View().Content))
			})
		}
	}
}

var goldenStopPlan = controller.ClosePlan{
	TaskID: "auth-tests", Worktree: "/src/repo-worktrees/auth-tests", Branch: "agentctl/auth-tests", Stop: true,
	HasSession: true, SessionName: "agentctl-auth-tests", Origin: domain.OriginLaunched, AgentState: domain.StateAwaitingInput,
	Identity: domain.Identity{Socket: "/tmp/tmux-501/default", ServerPID: 100, SessionID: "$1", PaneID: "%1", PanePID: 4000},
}
