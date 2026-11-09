package controller

import (
	"reflect"
	"strings"
	"testing"
)

// Process tables below are shaped like `ps -A -o pid,ppid,pgid,tpgid,tty,args`
// on a machine where tmux reports Claude Code's pane_current_command as its
// version string (the launcher is a symlink into claude/versions/<ver>).
func TestPaneRunsClaude(t *testing.T) {
	t.Parallel()
	const pane = 100
	shell := proc{PID: pane, PPID: 1, PGID: pane, TPGID: 200, TTY: "ttys004", Args: "-zsh"}
	idleShell := proc{PID: pane, PPID: 1, PGID: pane, TPGID: pane, TTY: "ttys004", Args: "-zsh"}
	fg := func(args string) proc {
		return proc{PID: 200, PPID: pane, PGID: 200, TPGID: 200, TTY: "ttys004", Args: args}
	}
	cases := []struct {
		name  string
		table []proc
		want  bool
	}{
		{"claude via the launcher symlink under a shell", []proc{shell, fg("claude")}, true},
		{"claude launched by absolute launcher path", []proc{shell, fg("/home/u/.local/bin/claude --resume")}, true},
		{"claude run from its versions directory", []proc{shell, fg("/home/u/.local/share/claude/versions/2.1.319")}, true},
		{"claude is the pane process itself", []proc{{PID: pane, PPID: 1, PGID: pane, TPGID: pane, TTY: "pts/3", Args: "/opt/bin/claude"}}, true},
		{"node wrapper around an npm claude", []proc{shell, fg("node /usr/local/bin/claude")}, true},
		{"claude's own tool child shares its group", []proc{shell, fg("claude"), {PID: 300, PPID: 200, PGID: 200, TPGID: 200, TTY: "ttys004", Args: "/bin/zsh -c npm test"}}, true},
		{"plain idle shell", []proc{idleShell}, false},
		{"unrelated version-named binary", []proc{shell, fg("/opt/tools/other/8.8.8 --serve")}, false},
		{"version dir not under claude", []proc{shell, fg("/home/u/.local/share/notclaude/versions/2.1.319")}, false},
		{"editor opened on a file named claude", []proc{shell, fg("vim claude")}, false},
		{"similar but different name", []proc{shell, fg("/usr/bin/claudette")}, false},
		{"claude in the background, shell in front", []proc{idleShell, {PID: 200, PPID: pane, PGID: 200, TPGID: pane, TTY: "ttys004", Args: "claude"}}, false},
		{"claude on another terminal", []proc{idleShell, {PID: 500, PPID: 1, PGID: 500, TPGID: 500, TTY: "ttys009", Args: "claude"}}, false},
		{"pane pid missing from the table", []proc{fg("claude")}, false},
		{"no foreground group known: descendant claude", []proc{{PID: pane, PPID: 1, PGID: pane, TPGID: -1, Args: "sh"}, {PID: 201, PPID: pane, PGID: pane, TPGID: -1, Args: "claude"}}, true},
		{"no foreground group known: unrelated process", []proc{{PID: pane, PPID: 1, PGID: pane, TPGID: 0, Args: "sh"}, {PID: 201, PPID: 7, PGID: 201, TPGID: 0, Args: "claude"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := paneRunsClaude(newProcTable(tc.table), pane); got != tc.want {
				t.Fatalf("paneRunsClaude = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsClaudeArgs(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"claude":                                      true,
		"claude --dangerously-skip-permissions":       true,
		"/Users/u/.local/bin/claude":                  true,
		"/Users/u/.local/share/claude/versions/2.1.3": true,
		"claude/versions/2.1.3":                       true,
		"bun /x/claude":                               true,
		"node /x/claude/versions/1/cli.js":            true,
		"":                                            false,
		"2.1.319":                                     false,
		"zsh":                                         false,
		"cat /x/claude":                               false,
		"less /x/claude/versions/1/README":            false,
		"/x/myclaude/versions/1":                      false,
		"node":                                        false,
	}
	for args, want := range cases {
		if got := isClaudeArgs(args); got != want {
			t.Errorf("isClaudeArgs(%q) = %v, want %v", args, got, want)
		}
	}
}

func TestParsePS(t *testing.T) {
	t.Parallel()
	out := strings.Join([]string{
		"    1     0     1     0 ??       /sbin/launchd",
		"97083 10946 97083 97083 ttys004  claude",
		"  412   400   412   415 pts/3    /bin/bash --login -i",
		"  999     1   999    -1 ?        /usr/lib/x/a path with  two  spaces",
		"  555     1   555     0 -        ",
		"",
	}, "\n")
	got, err := parsePS([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []proc{
		{PID: 1, PPID: 0, PGID: 1, TPGID: 0, TTY: "", Args: "/sbin/launchd"},
		{PID: 97083, PPID: 10946, PGID: 97083, TPGID: 97083, TTY: "ttys004", Args: "claude"},
		{PID: 412, PPID: 400, PGID: 412, TPGID: 415, TTY: "pts/3", Args: "/bin/bash --login -i"},
		{PID: 999, PPID: 1, PGID: 999, TPGID: -1, TTY: "", Args: "/usr/lib/x/a path with  two  spaces"},
		{PID: 555, PPID: 1, PGID: 555, TPGID: 0, TTY: "", Args: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePS =\n%+v\nwant\n%+v", got, want)
	}
	for _, bad := range []string{"12 x 3 4 tty args", "12 3 4"} {
		if _, err := parsePS([]byte(bad)); err == nil {
			t.Errorf("parsePS(%q) succeeded, want an error", bad)
		}
	}
}
