package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jffypak/agentctl/internal/adapter/claude"
	"github.com/jffypak/agentctl/internal/testenv"
)

// TestParsesGeneratedSettings is the contract test: fakeagent finds exactly
// the generator's one command hook (timeout 5) for every mapped event, for
// any matcher value.
func TestParsesGeneratedSettings(t *testing.T) {
	home := testenv.TempHome(t)
	const exe = "/opt/agent ctl/bin/agentctl"
	path, err := claude.New(claude.Config{AgentctlPath: exe}).EnsureSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range claude.HookEvents() {
		for _, v := range []string{"", "Bash", "AskUserQuestion", "permission_prompt", "startup", "compact", "other", "auto"} {
			hs := s.commands(ev, v)
			if len(hs) != 1 || hs[0].Command != claude.HookCommand(exe) || hs[0].Timeout != 5 {
				t.Fatalf("%s/%q: handlers = %+v", ev, v, hs)
			}
		}
	}
	if hs := s.commands("SubagentStop", ""); len(hs) != 0 {
		t.Fatalf("unmapped event has handlers: %+v", hs)
	}
}

func TestMatcher(t *testing.T) {
	s := settings{Hooks: map[string][]matcherGroup{
		"PreToolUse": {{Matcher: "Edit|Write", Hooks: []hookHandler{{Type: "command", Command: "a"}}},
			{Matcher: "*", Hooks: []hookHandler{{Type: "command", Command: "b"}, {Type: "prompt", Command: "x"}}}},
		"Stop": {{Matcher: "ignored", Hooks: []hookHandler{{Type: "command", Command: "c"}}}},
	}}
	names := func(hs []hookHandler) string {
		var out []string
		for _, h := range hs {
			out = append(out, h.Command)
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct{ event, value, want string }{
		{"PreToolUse", "Write", "a,b"}, {"PreToolUse", "Bash", "b"}, {"PreToolUse", "WriteX", "b"}, {"Stop", "", "c"},
	} {
		if got := names(s.commands(c.event, c.value)); got != c.want {
			t.Errorf("%s/%s = %q, want %q", c.event, c.value, got, c.want)
		}
	}
}

func TestTokenizer(t *testing.T) {
	type tk = token
	cases := []struct {
		name   string
		chunks []string
		want   []tk
	}{
		{"typed line", []string{"hi\r"}, []tk{{tokText, "hi"}, {tokEnter, ""}}},
		{"crlf is one enter", []string{"a\r\nb\n"}, []tk{{tokText, "a"}, {tokEnter, ""}, {tokText, "b"}, {tokEnter, ""}}},
		{"paste with CR lines", []string{"\x1b[200~one\rtwo\r\nthree\x1b[201~\r"}, []tk{{tokPaste, "one\ntwo\nthree"}, {tokEnter, ""}}},
		{"paste split across reads", []string{"x\x1b[20", "0~ab", "c\x1b[2", "01~y"}, []tk{{tokText, "x"}, {tokPaste, "abc"}, {tokText, "y"}}},
		{"arrows ignored", []string{"\x1b[A\x1b[Bq"}, []tk{{tokText, "q"}}},
		{"controls", []string{"a\x7f\x03\x04\x01"}, []tk{{tokText, "a"}, {tokBackspace, ""}, {tokInterrupt, ""}, {tokEOF, ""}}},
		{"utf8", []string{"héllo"}, []tk{{tokText, "héllo"}}},
	}
	for _, c := range cases {
		var tz tokenizer
		var got []tk
		for _, ch := range c.chunks {
			got = append(got, tz.Feed([]byte(ch))...)
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: tokens %+v, want %+v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: token %d = %+v, want %+v", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestParseDirectives(t *testing.T) {
	d := parseDirectives("please #ask then #sleep 25 and #exit 3")
	if !d.ask || d.exit != 3 || d.sleep != 25*time.Millisecond {
		t.Fatalf("directives = %+v", d)
	}
	if d := parseDirectives("#exit x #sleep -1"); d.ask || d.exit != -1 || d.sleep != -1 {
		t.Fatalf("invalid directives = %+v", d)
	}
}

func TestParseConfig(t *testing.T) {
	env := map[string]string{"FAKEAGENT_WORK_MS": "1500", "FAKEAGENT_LOG": "/l", "FAKEAGENT_PASTE_PLACEHOLDER": "1"}
	cfg, err := parseConfig([]string{"--settings", "/s.json", "fix it"}, func(k string) string { return env[k] })
	if err != nil || cfg.settingsPath != "/s.json" || cfg.initialPrompt != "fix it" || cfg.work != 1500*time.Millisecond ||
		cfg.logPath != "/l" || !cfg.pastePlaceholder {
		t.Fatalf("cfg = %+v %v", cfg, err)
	}
	if _, err := parseConfig(nil, func(k string) string { return map[string]string{"FAKEAGENT_WORK_MS": "x"}[k] }); err == nil {
		t.Fatal("bad FAKEAGENT_WORK_MS accepted")
	}
	if _, err := parseConfig([]string{"a", "b"}, func(string) string { return "" }); err == nil {
		t.Fatal("two prompts accepted")
	}
	cfg, err = parseConfig(nil, func(k string) string { return map[string]string{"FAKEAGENT_GOAL_DELAY_MS": "250"}[k] })
	if err != nil || cfg.goalDelay != 250*time.Millisecond {
		t.Fatalf("goal delay: cfg = %+v %v", cfg, err)
	}
	if _, err := parseConfig(nil, func(k string) string { return map[string]string{"FAKEAGENT_GOAL_DELAY_MS": "-1"}[k] }); err == nil {
		t.Fatal("bad FAKEAGENT_GOAL_DELAY_MS accepted")
	}
}

// harness drives an in-process agent through a pipe with real hook
// subprocesses that record their payloads.
type harness struct {
	t    *testing.T
	dir  string
	in   *io.PipeWriter
	out  *syncBuffer
	done chan struct{} // closed when run returns
	code int           // run's exit code, valid after done
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startAgent writes a settings file whose every mapped event appends its
// payload to hooks.jsonl; the Stop hook also marks its start and sleeps
// stopDelay before returning.
func startAgent(t *testing.T, prompt string, mod func(*config)) *harness {
	t.Helper()
	dir := testenv.TempHome(t)
	rec := "cat >> '" + filepath.Join(dir, "hooks.jsonl") + "'; echo >> '" + filepath.Join(dir, "hooks.jsonl") + "'"
	hooks := map[string][]matcherGroup{}
	for _, ev := range claude.HookEvents() {
		cmd := rec
		if ev == "Stop" {
			cmd = rec + "; touch '" + filepath.Join(dir, "stop.started") + "'; sleep 0.3"
		}
		hooks[ev] = []matcherGroup{{Hooks: []hookHandler{{Type: "command", Command: cmd, Timeout: 5}}}}
	}
	b, _ := json.Marshal(settings{Hooks: hooks})
	settingsPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(settingsPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config{
		settingsPath: settingsPath, initialPrompt: prompt, work: 20 * time.Millisecond,
		logPath: filepath.Join(dir, "input.jsonl"), hookLogPath: filepath.Join(dir, "hookruns.jsonl"),
		cwd: dir, env: testenv.NewEnv(t).Strings(), transcriptDir: filepath.Join(dir, "transcripts"),
	}
	if mod != nil {
		mod(&cfg)
	}
	out := &syncBuffer{}
	a, err := newAgent(cfg, out, false)
	if err != nil {
		t.Fatal(err)
	}
	pr, pw := io.Pipe()
	h := &harness{t: t, dir: dir, in: pw, out: out, done: make(chan struct{})}
	go a.readInput(pr)
	go func() {
		defer close(h.done)
		h.code = a.run()
	}()
	// Registered after TempHome's cleanup, so it runs first: the agent (and
	// its SessionEnd hook) is gone before the temp dir is removed.
	t.Cleanup(func() {
		_ = pw.Close()
		select {
		case <-h.done:
		case <-time.After(10 * time.Second):
			t.Error("agent did not exit after stdin closed")
		}
	})
	return h
}

// waitReady waits for the "> " prompt the agent prints once it is ready.
func (h *harness) waitReady() {
	h.t.Helper()
	testenv.WaitFor(h.t, 0, "ready prompt", func() (bool, string) {
		return strings.HasSuffix(h.out.String(), "> "), h.out.String()
	})
}

// exit waits for run to return and gives its code.
func (h *harness) exit() int {
	h.t.Helper()
	select {
	case <-h.done:
		return h.code
	case <-time.After(5 * time.Second):
		h.t.Fatal("agent did not exit")
	}
	return -1
}

func (h *harness) send(s string) {
	h.t.Helper()
	if _, err := h.in.Write([]byte(s)); err != nil {
		h.t.Fatal(err)
	}
}

func readJSONL(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("%s: %v: %q", path, err, sc.Text())
		}
		out = append(out, m)
	}
	return out
}

// events returns the hook_event_name sequence (with the matcher field).
func (h *harness) events() []string {
	var out []string
	for _, p := range readJSONL(h.t, filepath.Join(h.dir, "hooks.jsonl")) {
		e := p["hook_event_name"].(string)
		for _, k := range []string{"tool_name", "notification_type", "source", "reason"} {
			if v, ok := p[k].(string); ok {
				e += ":" + v
			}
		}
		out = append(out, e)
	}
	return out
}

func (h *harness) waitEvents(want string) {
	h.t.Helper()
	testenv.WaitFor(h.t, 0, "hook sequence "+want, func() (bool, string) {
		got := strings.Join(h.events(), " ")
		return got == want, got
	})
}

func TestAgentTurnAskExit(t *testing.T) {
	h := startAgent(t, "hello", nil)
	turn := "UserPromptSubmit PreToolUse:Bash PostToolUse:Bash Stop"
	seq := "SessionStart:startup " + turn
	h.waitEvents(seq)
	h.waitReady()
	if !strings.HasPrefix(h.out.String(), "\x1b[?2004h") {
		t.Fatalf("bracketed paste not enabled first: %q", h.out.String())
	}
	payloads := readJSONL(t, filepath.Join(h.dir, "hooks.jsonl"))
	for _, p := range payloads {
		if p["session_id"] == "" || p["cwd"] != h.dir || p["transcript_path"] == nil {
			t.Fatalf("payload missing common fields: %v", p)
		}
	}
	if payloads[1]["prompt"] != "hello" {
		t.Fatalf("UserPromptSubmit = %v", payloads[1])
	}

	h.send("#ask\r")
	seq += " UserPromptSubmit PreToolUse:Bash PermissionRequest:Bash Notification:permission_prompt"
	h.waitEvents(seq)
	h.send("y")
	seq += " PostToolUse:Bash " + turn[len("UserPromptSubmit "):]
	h.waitEvents(seq)
	h.waitReady()

	h.send("#exit 3\r")
	if code := h.exit(); code != 3 {
		t.Fatalf("exit code %d", code)
	}
	if got := strings.Join(h.events(), " "); !strings.HasSuffix(got, "UserPromptSubmit SessionEnd:other") {
		t.Fatalf("events = %s", got)
	}
	// the dialog key was logged as received while awaiting input
	var keys []map[string]any
	for _, e := range readJSONL(t, filepath.Join(h.dir, "input.jsonl")) {
		if e["kind"] == "key" {
			keys = append(keys, e)
		}
	}
	if len(keys) != 1 || keys[0]["text"] != "y" || keys[0]["state_at_receipt"] != stAwaiting {
		t.Fatalf("key entries = %v", keys)
	}
	for _, r := range readJSONL(t, filepath.Join(h.dir, "hookruns.jsonl")) {
		if r["exit"].(float64) != 0 || r["stdout"] != "" {
			t.Fatalf("hook run = %v", r)
		}
	}
}

// TestReadyOnlyAfterStopReturns: input arriving while the Stop hook is still
// running is received in state working; after it returns, in state ready.
func TestReadyOnlyAfterStopReturns(t *testing.T) {
	h := startAgent(t, "go", nil)
	testenv.WaitFor(t, 0, "Stop hook started", func() (bool, string) {
		_, err := os.Stat(filepath.Join(h.dir, "stop.started"))
		return err == nil, ""
	})
	h.send("\x1b[200~during stop\x1b[201~")
	h.waitReady()
	h.send("\x1b[200~after stop\x1b[201~")
	testenv.WaitFor(t, 0, "two pastes logged", func() (bool, string) {
		return len(readJSONL(t, filepath.Join(h.dir, "input.jsonl"))) == 2, ""
	})
	log := readJSONL(t, filepath.Join(h.dir, "input.jsonl"))
	if log[0]["text"] != "during stop" || log[0]["state_at_receipt"] != stWorking || log[0]["kind"] != "paste" {
		t.Fatalf("first = %v", log[0])
	}
	if log[1]["text"] != "after stop" || log[1]["state_at_receipt"] != stReady {
		t.Fatalf("second = %v", log[1])
	}
	for _, k := range []string{"ts", "text", "state_at_receipt"} {
		if _, ok := log[0][k]; !ok {
			t.Fatalf("log entry lacks %q: %v", k, log[0])
		}
	}
}

func TestPastePlaceholderAndSessionEndOnEOF(t *testing.T) {
	h := startAgent(t, "", func(c *config) { c.pastePlaceholder = true })
	h.waitEvents("SessionStart:startup")
	h.waitReady()
	h.send("\x1b[200~line one\rline two\x1b[201~\r")
	h.waitEvents("SessionStart:startup UserPromptSubmit PreToolUse:Bash PostToolUse:Bash Stop")
	payloads := readJSONL(t, filepath.Join(h.dir, "hooks.jsonl"))
	if payloads[1]["prompt"] != "[Pasted text #1]" {
		t.Fatalf("UserPromptSubmit prompt = %v", payloads[1]["prompt"])
	}
	log := readJSONL(t, filepath.Join(h.dir, "input.jsonl"))
	if len(log) != 2 || log[0]["kind"] != "paste" || log[0]["text"] != "line one\nline two" ||
		log[1]["kind"] != "submit" || log[1]["text"] != "line one\nline two" || log[1]["state_at_receipt"] != stReady {
		t.Fatalf("input log = %v", log)
	}
	h.waitReady()
	_ = h.in.Close()
	if code := h.exit(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if ev := h.events(); ev[len(ev)-1] != "SessionEnd:prompt_input_exit" {
		t.Fatalf("events = %v", ev)
	}
}

func TestSleepDirectiveOverridesWork(t *testing.T) {
	h := startAgent(t, "", func(c *config) { c.work = time.Hour })
	h.waitEvents("SessionStart:startup")
	h.waitReady()
	start := time.Now()
	h.send("#sleep 10\r")
	h.waitEvents("SessionStart:startup UserPromptSubmit PreToolUse:Bash PostToolUse:Bash Stop")
	if time.Since(start) > 3*time.Second {
		t.Fatal("#sleep did not override FAKEAGENT_WORK_MS")
	}
}

// TestGoalDelayKeepsStartingUntilSubmit (m3-goal-start-race): with
// FAKEAGENT_GOAL_DELAY_MS, the initial prompt is submitted that long after
// the SessionStart hook returns, like real Claude Code's ~230 ms gap. Input
// arriving in the gap is logged as received while starting (never ready),
// and the goal turn still runs.
func TestGoalDelayKeepsStartingUntilSubmit(t *testing.T) {
	h := startAgent(t, "go", func(c *config) { c.goalDelay = 1500 * time.Millisecond })
	h.waitEvents("SessionStart:startup")
	h.send("\x1b[200~in the gap\x1b[201~")
	testenv.WaitFor(t, 0, "the paste logged", func() (bool, string) {
		return len(readJSONL(t, filepath.Join(h.dir, "input.jsonl"))) == 1, ""
	})
	if got := strings.Join(h.events(), " "); got != "SessionStart:startup" {
		t.Fatalf("the goal was submitted before the delay elapsed: %s", got)
	}
	if log := readJSONL(t, filepath.Join(h.dir, "input.jsonl")); log[0]["state_at_receipt"] != stStarting {
		t.Fatalf("paste in the gap = %v, want received while starting", log[0])
	}
	h.waitEvents("SessionStart:startup UserPromptSubmit PreToolUse:Bash PostToolUse:Bash Stop")
	if p := readJSONL(t, filepath.Join(h.dir, "hooks.jsonl")); p[1]["prompt"] != "go" {
		t.Fatalf("UserPromptSubmit = %v", p[1])
	}
}
