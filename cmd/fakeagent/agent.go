package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Agent states as fakeagent itself sees them (recorded as state_at_receipt).
const (
	stStarting = "starting"
	stReady    = "ready"
	stWorking  = "working"
	stAwaiting = "awaiting_input"
)

// config is fakeagent's flags and FAKEAGENT_* environment.
type config struct {
	settingsPath     string
	initialPrompt    string
	work             time.Duration // FAKEAGENT_WORK_MS (default 300)
	logPath          string        // FAKEAGENT_LOG: JSONL of every received input
	hookLogPath      string        // FAKEAGENT_HOOK_LOG: JSONL of every hook run (stdout, exit, ms)
	pastePlaceholder bool          // FAKEAGENT_PASTE_PLACEHOLDER=1
	trustDialog      bool          // FAKEAGENT_TRUST_DIALOG=1: folder-trust dialog before SessionStart (trust.go)
	goalDelay        time.Duration // FAKEAGENT_GOAL_DELAY_MS: SessionStart -> initial-prompt submit gap (goal.go)
	cwd              string
	env              []string // hooks inherit this environment
	transcriptDir    string   // transcript_path dir (never created; agentctl must not open it)
}

type eventKind int

const (
	evSubmit  eventKind = iota // Enter while not in a dialog
	evApprove                  // y / Enter in the permission dialog
	evDeny                     // n in the permission dialog
	evQuit                     // ctrl-c, ctrl-d, or stdin closed
)

// event is what the input goroutine hands the agent loop.
type event struct {
	kind     eventKind
	text     string // the submitted text, pastes expanded
	hookText string // what UserPromptSubmit reports (placeholder mode: [Pasted text #N])
	state    string // fakeagent state when the input arrived
}

type agent struct {
	cfg       config
	hooks     settings
	term      *terminal
	inputLog  *jsonl
	hookLog   *jsonl
	sessionID string
	events    chan event

	mu    sync.Mutex
	state string

	pasteN int // input goroutine only
}

func newAgent(cfg config, out io.Writer, raw bool) (*agent, error) {
	s, err := loadSettings(cfg.settingsPath)
	if err != nil {
		return nil, err
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	h := hex.EncodeToString(id[:])
	return &agent{
		cfg: cfg, hooks: s, term: &terminal{w: out, raw: raw},
		inputLog: &jsonl{path: cfg.logPath}, hookLog: &jsonl{path: cfg.hookLogPath},
		sessionID: h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:],
		events:    make(chan event, 64),
		state:     stStarting,
	}, nil
}

func (a *agent) getState() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *agent) setState(s string) {
	a.mu.Lock()
	a.state = s
	a.mu.Unlock()
}

// run is the agent loop. It returns the process exit code.
func (a *agent) run() int {
	a.term.write("\x1b[?2004h") // bracketed paste on
	if code, ok := a.trustGate(); !ok {
		return code
	}
	a.term.write("fakeagent " + a.sessionID + "\n")
	a.fire("SessionStart", map[string]any{"source": "startup", "model": "fakeagent"})
	if a.cfg.initialPrompt == "" {
		a.setState(stReady)
	} else {
		a.awaitGoalSubmit() // goal.go: still starting until the goal turn begins
		if code, done := a.turn(a.cfg.initialPrompt, a.cfg.initialPrompt); done {
			return code
		}
	}
	a.prompt()
	for ev := range a.events {
		switch ev.kind {
		case evQuit:
			return a.end("prompt_input_exit", 0)
		case evSubmit:
			if ev.state != stReady {
				a.term.write("(input ignored: it arrived while " + ev.state + ")\n")
				a.prompt()
				continue
			}
			if code, done := a.turn(ev.text, ev.hookText); done {
				return code
			}
			a.prompt()
		}
	}
	return a.end("other", 0)
}

func (a *agent) prompt() { a.term.write("> ") }

// end fires SessionEnd and returns code.
func (a *agent) end(reason string, code int) int {
	a.fire("SessionEnd", map[string]any{"reason": reason})
	return code
}

// turn handles one submitted prompt: UserPromptSubmit -> directives / work
// (PreToolUse, PostToolUse) -> Stop, and flips to ready only after the Stop
// hook has returned. done reports that the agent must exit with code.
func (a *agent) turn(text, hookText string) (code int, done bool) {
	a.setState(stWorking)
	a.fire("UserPromptSubmit", map[string]any{"prompt": hookText})
	d := parseDirectives(text)
	if d.exit >= 0 {
		a.term.write(fmt.Sprintf("exiting with %d\n", d.exit))
		return a.end("other", d.exit), true
	}
	if d.ask {
		approved, quit := a.ask()
		if quit {
			return a.end("prompt_input_exit", 0), true
		}
		a.setState(stWorking)
		if approved {
			a.fire("PostToolUse", bashTool("tool_response", map[string]any{"stdout": "approved", "exit_code": 0}))
		} else {
			a.term.write("denied\n")
		}
	}
	work := a.cfg.work
	if d.sleep >= 0 {
		work = d.sleep
	}
	a.fire("PreToolUse", bashTool())
	a.term.write(fmt.Sprintf("working for %v (Bash)...\n", work))
	time.Sleep(work)
	a.fire("PostToolUse", bashTool("tool_response", map[string]any{"stdout": "ok", "exit_code": 0}))
	a.term.write("done\n")
	a.fire("Stop", map[string]any{"stop_hook_active": false, "last_assistant_message": "done"})
	a.setState(stReady)
	return 0, false
}

// ask emulates a Bash permission dialog: PreToolUse, PermissionRequest and
// Notification permission_prompt, then waits for y (or Enter, the default
// "Yes" — which is why nothing may ever be pasted into a dialog) or n.
func (a *agent) ask() (approved, quit bool) {
	a.fire("PreToolUse", bashTool())
	a.fire("PermissionRequest", bashTool())
	a.setState(stAwaiting)
	a.fire("Notification", map[string]any{
		"notification_type": "permission_prompt", "title": "Permission needed",
		"message": "fakeagent needs your permission to use Bash",
	})
	a.term.write("Allow Bash: echo approved? (y/n) ")
	for ev := range a.events {
		switch ev.kind {
		case evApprove:
			a.term.write("yes\n")
			return true, false
		case evDeny:
			return false, false
		case evQuit:
			return false, true
		}
	}
	return false, true
}

func bashTool(extra ...any) map[string]any {
	m := map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "echo approved"}, "tool_use_id": "toolu_fake"}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

// directives found in a prompt: #ask, #exit N, #sleep MS.
type directives struct {
	ask   bool
	exit  int           // -1: none
	sleep time.Duration // -1: none
}

func parseDirectives(text string) directives {
	d := directives{exit: -1, sleep: -1}
	f := strings.Fields(text)
	for i, w := range f {
		switch w {
		case "#ask":
			d.ask = true
		case "#exit", "#sleep":
			if i+1 >= len(f) {
				continue
			}
			n, err := strconv.Atoi(f[i+1])
			if err != nil || n < 0 {
				continue
			}
			if w == "#exit" {
				d.exit = n
			} else {
				d.sleep = time.Duration(n) * time.Millisecond
			}
		}
	}
	return d
}

// readInput tokenizes terminal input and edits the prompt line. Every
// received input is logged with the state at the moment it arrived, before
// the agent loop sees it: pastes as kind "paste", Enter as "submit" (with the
// whole line), and dialog keys as "key".
func (a *agent) readInput(r io.Reader) {
	var tz tokenizer
	var line, hookLine strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		for _, tok := range tz.Feed(buf[:n]) {
			a.handle(tok, &line, &hookLine)
		}
		if err != nil {
			a.events <- event{kind: evQuit}
			return
		}
	}
}

func (a *agent) handle(tok token, line, hookLine *strings.Builder) {
	st := a.getState()
	if st == stTrust {
		a.trustKey(tok)
		return
	}
	switch tok.kind {
	case tokText:
		if st == stAwaiting {
			for _, r := range tok.text {
				a.inputLog.write(inputEntry(string(r), st, "key"))
				switch r {
				case 'y', 'Y':
					a.events <- event{kind: evApprove, state: st}
				case 'n', 'N':
					a.events <- event{kind: evDeny, state: st}
				}
			}
			return
		}
		line.WriteString(tok.text)
		hookLine.WriteString(tok.text)
		a.term.write(tok.text)
	case tokPaste:
		a.inputLog.write(inputEntry(tok.text, st, "paste"))
		if st == stAwaiting {
			return // a dialog has no text box: the paste is dropped (and logged)
		}
		line.WriteString(tok.text)
		shown := tok.text
		if a.cfg.pastePlaceholder {
			a.pasteN++
			shown = "[Pasted text #" + strconv.Itoa(a.pasteN) + "]"
		}
		hookLine.WriteString(shown)
		a.term.write(shown)
	case tokEnter:
		a.inputLog.write(inputEntry(line.String(), st, "submit"))
		if st == stAwaiting {
			a.events <- event{kind: evApprove, state: st} // Enter picks the default "Yes"
			return
		}
		a.term.write("\n")
		a.events <- event{kind: evSubmit, text: line.String(), hookText: hookLine.String(), state: st}
		line.Reset()
		hookLine.Reset()
	case tokBackspace:
		trimLastRune(line)
		trimLastRune(hookLine)
		a.term.write("\b \b")
	case tokInterrupt, tokEOF:
		a.events <- event{kind: evQuit, state: st}
	}
}

func trimLastRune(b *strings.Builder) {
	s := []rune(b.String())
	if len(s) == 0 {
		return
	}
	b.Reset()
	b.WriteString(string(s[:len(s)-1]))
}

func inputEntry(text, state, kind string) map[string]any {
	return map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "text": text, "state_at_receipt": state, "kind": kind}
}

// terminal serializes output; in raw mode "\n" must be written as "\r\n".
type terminal struct {
	mu  sync.Mutex
	w   io.Writer
	raw bool
}

func (t *terminal) write(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.raw {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	_, _ = io.WriteString(t.w, s)
}

// jsonl appends one JSON object per line to path ("" = disabled).
type jsonl struct {
	mu   sync.Mutex
	path string
}

func (j *jsonl) write(v any) {
	if j.path == "" {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakeagent: log %s: %v\n", j.path, err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
