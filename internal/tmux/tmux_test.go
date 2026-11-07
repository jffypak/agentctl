package tmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEmptySocketIsAnError(t *testing.T) {
	if _, err := Argv(Request{Commands: [][]string{{"list-panes"}}}, ""); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Argv with empty socket = %v, want ErrNoSocket", err)
	}
	// The exec runner must refuse before exec'ing anything (Bin does not exist).
	r := ExecRunner{Bin: "/nonexistent/tmux-binary"}
	_, err := r.Run(context.Background(), Request{Commands: [][]string{{"list-panes"}}})
	if !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Run with empty socket = %v, want ErrNoSocket", err)
	}
	c := NewClient(r, Socket{})
	if _, err := c.Snapshot(context.Background()); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("Snapshot with empty socket = %v, want ErrNoSocket", err)
	}
	if _, err := Argv(Request{Socket: Socket{Path: "relative/sock"}, Commands: [][]string{{"x"}}}, ""); err == nil {
		t.Fatal("relative -S path accepted")
	}
	if _, err := Argv(Request{Socket: Socket{Path: "/a", Name: "b"}, Commands: [][]string{{"x"}}}, ""); err == nil {
		t.Fatal("both -S and -L accepted")
	}
	if _, err := Argv(Request{Socket: Socket{Path: "/a"}}, ""); err == nil {
		t.Fatal("empty command list accepted")
	}
}

func TestArgvSocketConfigAndSeparators(t *testing.T) {
	got, err := Argv(Request{
		Socket:   Socket{Path: "/tmp/tmux-1/default"},
		Commands: [][]string{{"new-session", "-d", "goal;"}, {"set-option", "-w", "remain-on-exit", "on"}},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-S", "/tmp/tmux-1/default", "new-session", "-d", `goal\;`, ";", "set-option", "-w", "remain-on-exit", "on"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Argv = %q\nwant   %q", got, want)
	}
	got, _ = Argv(Request{Socket: Socket{Name: "agentctl-test-x"}, Commands: [][]string{{"has-session"}}}, "/dev/null")
	want = []string{"-L", "agentctl-test-x", "-f", "/dev/null", "has-session"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Argv = %q, want %q", got, want)
	}
}

func TestEscapeArg(t *testing.T) {
	cases := map[string]string{
		"plain":  "plain",
		"":       "",
		"a;":     `a\;`,
		";":      `\;`,
		`a\;`:    `a\\;`,
		"a;b":    "a;b", // only a trailing ';' ends a command
		"{":      "{",
		"#{x}":   "#{x}",
		"semi; ": "semi; ",
	}
	for in, want := range cases {
		if got := EscapeArg(in); got != want {
			t.Errorf("EscapeArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEscapeFormat(t *testing.T) {
	if got := EscapeFormat("/a/#(touch x)#{pane_id}"); got != "/a/##(touch x)##{pane_id}" {
		t.Fatalf("EscapeFormat = %q", got)
	}
}

func TestNewSessionCommands(t *testing.T) {
	cmds, err := NewSessionCommands(NewSessionSpec{
		Name: "agentctl-demo", Dir: "/w/#demo",
		Env:  []string{"AGENTCTL_TASK_ID=demo", "AGENTCTL_HOME=/h"},
		Argv: []string{"/bin/claude", "--settings", "/h/hooks/claude-settings.json", "fix it; now"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"new-session", "-d", "-P", "-F", "#{pid}\t#{session_id}\t#{pane_id}\t#{pane_pid}", "-s", "agentctl-demo",
			"-c", "/w/##demo", "-e", "AGENTCTL_TASK_ID=demo", "-e", "AGENTCTL_HOME=/h",
			"--", "/bin/claude", "--settings", "/h/hooks/claude-settings.json", "fix it; now"},
		{"set-option", "-w", "remain-on-exit", "on"},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("NewSessionCommands =\n%q\nwant\n%q", cmds, want)
	}

	bad := []NewSessionSpec{
		{Name: "", Dir: "/w", Argv: []string{"a", "b"}},
		{Name: "a:b", Dir: "/w", Argv: []string{"a", "b"}},
		{Name: "a.b", Dir: "/w", Argv: []string{"a", "b"}},
		{Name: "ok", Dir: "", Argv: []string{"a", "b"}},
		{Name: "ok", Dir: "rel", Argv: []string{"a", "b"}},
		{Name: "ok", Dir: "/w", Argv: []string{"sh -c 'evil'"}}, // one element => tmux uses sh -c
		{Name: "ok", Dir: "/w", Argv: nil},
		{Name: "ok", Dir: "/w", Argv: []string{"a", "b"}, Env: []string{"NOEQUALS"}},
		{Name: "ok", Dir: "/w", Argv: []string{"a", "b"}, Env: []string{"=v"}},
		{Name: "ok", Dir: "/w", Argv: []string{"a", "b\x00"}},
	}
	for _, s := range bad {
		if _, err := NewSessionCommands(s); err == nil {
			t.Errorf("NewSessionCommands(%+v) accepted", s)
		}
	}
}

func TestParseCreated(t *testing.T) {
	c, err := parseCreated([]byte("100\t$3\t%7\t4242\n"))
	if err != nil || c != (Created{ServerPID: 100, SessionID: "$3", PaneID: "%7", PanePID: 4242}) {
		t.Fatalf("parseCreated = %+v %v", c, err)
	}
	for _, in := range []string{"", "\n", "$3\t%7\t1", "100\t$3\t%7", "100\t$3\t%7\tx", "x\t$3\t%7\t1", "100\t3\t%7\t1", "100\t$3\t7\t1"} {
		if _, err := parseCreated([]byte(in)); err == nil {
			t.Errorf("parseCreated(%q) accepted", in)
		}
	}
}

func TestParseSnapshot(t *testing.T) {
	out := "4100\n" +
		"4100\t$0\t0\t%0\t4200\t0\t\t\tagentctl-demo\tclaude\t/w/demo\n" +
		"4100\t$1\t2\t%5\t4300\t1\t3\t\tother\tsh\t/path/with\ttab\n" +
		"4100\t$2\t0\t%6\t4400\t1\t\t9\tsig\tsleep\t\n"
	snap, err := parseSnapshot([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if snap.ServerPID != 4100 || len(snap.Panes) != 3 {
		t.Fatalf("snapshot = %+v", snap)
	}
	p0 := snap.Panes[0]
	if p0 != (Pane{ServerPID: 4100, SessionID: "$0", WindowIndex: 0, PaneID: "%0", PanePID: 4200,
		SessionName: "agentctl-demo", CurrentCommand: "claude", CurrentPath: "/w/demo"}) {
		t.Fatalf("pane 0 = %+v", p0)
	}
	if p := snap.Panes[1]; !p.Dead || p.DeadStatus != 3 || p.WindowIndex != 2 || p.CurrentPath != "/path/with\ttab" {
		t.Fatalf("pane 1 = %+v (path keeps its tab because it is the last field)", p)
	}
	if p := snap.Panes[2]; !p.Dead || p.DeadSignal != 9 || p.DeadStatus != 0 {
		t.Fatalf("pane 2 = %+v", p)
	}
	if got, ok := snap.Find("%5"); !ok || got.PanePID != 4300 {
		t.Fatalf("Find(%%5) = %+v %v", got, ok)
	}
	if _, ok := snap.Find("%9"); ok {
		t.Fatal("Find(%9) found a pane")
	}
	empty, err := parseSnapshot([]byte("4100\n"))
	if err != nil || empty.ServerPID != 4100 || len(empty.Panes) != 0 {
		t.Fatalf("pid-only snapshot = %+v %v", empty, err)
	}
	for _, bad := range []string{"", "notapid\n", "4100\n4100\t$0\n", "4100\nx\t$0\t0\t%0\t1\t0\t\t\ts\tc\t/p\n"} {
		if _, err := parseSnapshot([]byte(bad)); err == nil {
			t.Errorf("parseSnapshot(%q) accepted", bad)
		}
	}
}

func TestObserve(t *testing.T) {
	snap := Snapshot{ServerPID: 10, Panes: []Pane{{ServerPID: 10, PaneID: "%1", PanePID: 20, Dead: true, DeadStatus: 3}}}
	obs := snap.Observe("%1")
	if !obs.ServerReachable || obs.ServerPID != 10 || !obs.Found || obs.PanePID != 20 || !obs.Dead || obs.DeadStatus != 3 {
		t.Fatalf("Observe = %+v", obs)
	}
	if obs := snap.Observe("%2"); obs.Found || !obs.ServerReachable {
		t.Fatalf("Observe missing = %+v", obs)
	}
}

func TestErrorClassification(t *testing.T) {
	for _, stderr := range []string{
		"no server running on /tmp/tmux-501/x",
		"error connecting to /tmp/tmux-501/x (No such file or directory)",
		"error connecting to /tmp/x (Connection refused)",
	} {
		err := newError([]string{"list-panes"}, stderr, errors.New("exit status 1"))
		if !errors.Is(err, ErrNoServer) {
			t.Errorf("%q not classified as ErrNoServer", stderr)
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("error is multi-line: %q", err)
		}
	}
	if err := newError([]string{"new-session"}, "duplicate session: x\n", errors.New("exit status 1")); errors.Is(err, ErrNoServer) {
		t.Error("duplicate session classified as ErrNoServer")
	} else if !strings.Contains(err.Error(), "duplicate session: x") {
		t.Errorf("error lost stderr: %q", err)
	}
}

func TestSocketFromTMUX(t *testing.T) {
	cases := map[string]string{
		"/private/tmp/tmux-501/default,1551,0": "/private/tmp/tmux-501/default",
		"/tmp/odd,name/sock,12,3":              "/tmp/odd,name/sock",
		"":                                     "",
		"garbage":                              "",
	}
	for in, want := range cases {
		if got := SocketFromTMUX(in); got != want {
			t.Errorf("SocketFromTMUX(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveSocket(t *testing.T) {
	tmp := t.TempDir()
	real, _ := filepath.EvalSymlinks(tmp)
	explicit := filepath.Join(tmp, "explicit.sock")
	env := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"explicit wins", map[string]string{"AGENTCTL_TMUX_SOCKET": explicit, "TMUX": "/other,1,0"}, filepath.Join(real, "explicit.sock")},
		{"from $TMUX", map[string]string{"TMUX": tmp + "/fromtmux,1,0"}, filepath.Join(real, "fromtmux")},
		{"default via TMUX_TMPDIR", map[string]string{"TMUX_TMPDIR": tmp}, filepath.Join(real, "tmux-501", "default")},
	}
	for _, c := range cases {
		got, err := ResolveSocket(env(c.env), 501)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: ResolveSocket = %q, want %q", c.name, got, c.want)
		}
	}
	got, err := ResolveSocket(env(nil), 501)
	if err != nil || !strings.HasSuffix(got, "/tmux-501/default") || !filepath.IsAbs(got) {
		t.Fatalf("default = %q %v", got, err)
	}
	if _, err := ResolveSocket(env(map[string]string{"AGENTCTL_TMUX_SOCKET": "rel/sock"}), 501); err == nil {
		t.Fatal("relative AGENTCTL_TMUX_SOCKET accepted")
	}
}

func TestCanonicalSocketAndSameSocket(t *testing.T) {
	tmp := t.TempDir()
	real, _ := filepath.EvalSymlinks(tmp)
	f := filepath.Join(tmp, "s")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := CanonicalSocket(f); got != filepath.Join(real, "s") {
		t.Fatalf("CanonicalSocket(existing) = %q", got)
	}
	if got := CanonicalSocket(filepath.Join(tmp, "missing")); got != filepath.Join(real, "missing") {
		t.Fatalf("CanonicalSocket(missing) = %q", got)
	}
	if !SameSocket(f, filepath.Join(real, "s")) {
		t.Fatal("symlinked and real paths must compare equal")
	}
	if SameSocket(f, filepath.Join(real, "t")) || SameSocket("", "") {
		t.Fatal("different/empty sockets compared equal")
	}
}
