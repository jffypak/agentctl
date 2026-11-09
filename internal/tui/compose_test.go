package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/jffypak/agentctl/internal/domain"
	"github.com/jffypak/agentctl/internal/store"
)

var ctrlS = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}

// promptFixture: a launched task (fakeTmux pane, real worktree) moved to
// state by hook-like transitions.
func promptFixture(t *testing.T, to ...domain.AgentState) (*manageFixture, store.Session) {
	t.Helper()
	f := newManageFixture(t)
	f.newTask(t, "payments", "Refactor payment retries")
	sess, err := f.st.Sessions().LatestForTask(context.Background(), "payments")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range to {
		f.transition(t, sess.ID, s)
	}
	return f, sess
}

func (f *manageFixture) transition(t *testing.T, id int64, to domain.AgentState) {
	t.Helper()
	conf := domain.ConfidenceHook
	if to.IsTerminal() {
		conf = domain.ConfidencePane
	}
	if _, err := f.st.Sessions().Transition(context.Background(), id, to, conf, "", domain.SourceHook); err != nil {
		t.Fatal(err)
	}
}

func (f *manageFixture) messages(t *testing.T) []store.Message {
	t.Helper()
	ms, err := f.st.Messages().ForTask(context.Background(), "payments", 0)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func sendKey(t *testing.T, m Model) Model {
	t.Helper()
	next, cmd := m.Update(ctrlS)
	return runLong(t, next.(Model), cmd)
}

func TestComposerSendsQueuesAndTheDetailFollowsLive(t *testing.T) {
	t.Parallel()
	f, sess := promptFixture(t, domain.StateWorking)
	m := f.managed(t, 120, 40, Options{})
	m = press(t, m, "p")
	if _, ok := topView(m).(composeView); !ok {
		t.Fatalf("p did not open the composer:\n%s", screenText(m))
	}
	if !strings.Contains(screenText(m), "will be queued · agent working") {
		t.Fatalf("composer:\n%s", screenText(m))
	}
	m = typeText(t, m, "q j n x")     // keymap letters are text here
	m = update(t, m, keyMsg("enter")) // a new line, not a send
	m = update(t, m, tea.PasteMsg{Content: "pasted line"})
	if txt := screenText(m); !strings.Contains(txt, "q j n x") || !strings.Contains(txt, "pasted line") || !strings.Contains(txt, "2 lines") {
		t.Fatalf("composer text:\n%s", txt)
	}
	m = sendKey(t, m)
	if len(m.views) != 0 {
		t.Fatalf("composer still open:\n%s", screenText(m))
	}
	if !strings.Contains(screenText(m), "#1 queued · agent working") {
		t.Fatalf("flash:\n%s", screenText(m))
	}
	ms := f.messages(t)
	if len(ms) != 1 || ms[0].Body != "q j n x\npasted line" || ms[0].State != domain.MsgQueued {
		t.Fatalf("messages = %+v", ms)
	}
	if !slices.Contains(uiActions(t, f.ctl), "prompt:payments") {
		t.Fatalf("ui.action = %v", uiActions(t, f.ctl))
	}
	if slices.ContainsFunc(f.tm.calls, func(c string) bool { return c == "load-buffer" }) {
		t.Fatal("something was pasted into a working agent")
	}

	// the detail lists it with its live state
	m = pressRun(t, m, "enter")
	if !strings.Contains(screenText(m), "#1 queued · agent working · q j n x …") {
		t.Fatalf("detail:\n%s", screenText(m))
	}
	f.transition(t, sess.ID, domain.StateAwaitingInput)
	m = runLong(t, m, pollCmd(m.sh, m.lastID, true))
	if !strings.Contains(screenText(m), "#1 queued · awaiting input") {
		t.Fatalf("detail did not follow the state:\n%s", screenText(m))
	}
	if !strings.Contains(screenText(m), "c cancel queued") {
		t.Fatalf("hints:\n%s", screenText(m))
	}
	m = pressRun(t, m, "c")
	if txt := screenText(m); !strings.Contains(txt, "#1 cancelled") || !strings.Contains(txt, "messages   1 cancelled") {
		t.Fatalf("after cancel:\n%s", txt)
	}
	if ms := f.messages(t); ms[0].State != domain.MsgCancelled {
		t.Fatalf("message = %+v", ms[0])
	}
	m = pressRun(t, m, "c")
	if !strings.Contains(screenText(m), "only queued messages can be cancelled") {
		t.Fatalf("cancel of a cancelled message:\n%s", screenText(m))
	}
}

func TestComposerRefusalStaysInlineAndEscCancels(t *testing.T) {
	t.Parallel()
	f, _ := promptFixture(t, domain.StateWorking, domain.StateStopped)
	m := pressRun(t, f.managed(t, 80, 24, Options{}), "p")
	if !strings.Contains(screenText(m), "send will be refused") {
		t.Fatalf("composer:\n%s", screenText(m))
	}
	m = sendKey(t, m)
	if !strings.Contains(screenText(m), "type a prompt first") {
		t.Fatalf("empty send:\n%s", screenText(m))
	}
	m = typeText(t, m, "hello")
	m = sendKey(t, m)
	txt := screenText(m)
	if len(m.views) != 1 || !strings.Contains(txt, "! send rejected") || !strings.Contains(txt, "hello") {
		t.Fatalf("refused send:\n%s", txt)
	}
	m = press(t, m, "esc")
	if len(m.views) != 0 || len(f.messages(t)) != 0 || slices.Contains(uiActions(t, f.ctl), "prompt:payments") {
		t.Fatalf("esc: views %d, messages %d, actions %v", len(m.views), len(f.messages(t)), uiActions(t, f.ctl))
	}
}

// TestMessagesLoadedWhileAViewIsOpenReachTheDetail: the Model.forward fix —
// a detail section's answer arriving while the composer is on top is kept.
func TestMessagesLoadedWhileAViewIsOpenReachTheDetail(t *testing.T) {
	t.Parallel()
	f, sess := promptFixture(t, domain.StateWorking)
	if _, err := f.st.Messages().Enqueue(context.Background(), store.NewMessage{TaskID: "payments", SessionID: sess.ID, Body: "earlier prompt"}); err != nil {
		t.Fatal(err)
	}
	m, load := pressCmd(t, f.managed(t, 120, 40, Options{}), "enter")
	m = press(t, m, "p")
	m = runLong(t, m, load) // lands while the composer is on top
	m = press(t, m, "esc")
	if !strings.Contains(screenText(m), "#1 queued · agent working · earlier prompt") {
		t.Fatalf("the messages answer was lost:\n%s", screenText(m))
	}
}

// TestPromptGolden pins the composer and the messages section of the detail
// at both sizes. Update with: go test ./internal/tui -run TestPromptGolden -update
func TestPromptGolden(t *testing.T) {
	t.Parallel()
	screens := []struct {
		name  string
		build func(t *testing.T, w, h int) Model
	}{
		{"compose", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seed(t, everyState)
			m := press(t, f.model(t, w, h, Options{}), "j", "j", "j", "j", "j", "j", "j", "j", "p") // payments: working
			m = typeText(t, m, "Add retries with exponential backoff to the payment client.")
			m = update(t, m, keyMsg("enter"))
			return typeText(t, m, "Then run go test ./... and summarize failures.")
		}},
		{"detail_messages", func(t *testing.T, w, h int) Model {
			f := newFixture(t)
			f.seed(t, everyState)
			seedMessages(t, f, "payments")
			m := f.model(t, w, h, Options{})
			m.selected = "payments"
			m, cmd := pressCmd(t, m, "enter")
			for _, msg := range collect(cmd) {
				if _, ok := msg.(messagesLoadedMsg); ok {
					m = update(t, m, msg)
				}
			}
			return m
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

// seedMessages writes one message per delivery state for task.
func seedMessages(t *testing.T, f *fixture, task string) {
	t.Helper()
	sess, err := f.st.Sessions().LatestForTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	at := fixedNow.UnixMilli()
	for _, m := range []struct{ body, state, match string }{
		{"Fix the flaky retry test first", "acknowledged", "hash"},
		{"[a long multi-line paste]\nwith details", "acknowledged", "timing"},
		{"Did the migration run?", "unconfirmed", ""},
		{"Cancel that, use the v2 client", "cancelled", ""},
		{"Now add the docs\nand update the changelog", "queued", ""},
	} {
		f.exec(t, `INSERT INTO messages (task_id, session_id, body, body_sha256, state, ack_match, created_at, sent_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, task, sess.ID, m.body, store.BodyHash(m.body), m.state, m.match, at, at)
	}
}
