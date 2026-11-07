package pager

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func lookup(kv ...string) func(string) string {
	return func(k string) string {
		for i := len(kv) - 1; i >= 0; i-- {
			if key, v, ok := strings.Cut(kv[i], "="); ok && key == k {
				return v
			}
		}
		return ""
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"default", nil, "less -R"},
		{"PAGER", []string{"PAGER=more"}, "more"},
		{"AGENTCTL_PAGER wins", []string{"PAGER=more", "AGENTCTL_PAGER=delta --paging=always"}, "delta --paging=always"},
		{"blank AGENTCTL_PAGER falls through", []string{"PAGER=most", "AGENTCTL_PAGER=  "}, "most"},
		{"blank PAGER falls through", []string{"PAGER="}, "less -R"},
		{"trimmed", []string{"AGENTCTL_PAGER=  cat \n"}, "cat"},
	}
	for _, c := range cases {
		if got := Resolve(lookup(c.env...)); got != c.want {
			t.Errorf("%s: Resolve = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCommandRunsThroughShellWithGivenEnv(t *testing.T) {
	environ := []string{"PATH=/usr/bin:/bin", "AGENTCTL_PAGER=less -R"}
	cmd := Command(lookup(environ...), environ)
	if cmd.Path != "/bin/sh" || !reflect.DeepEqual(cmd.Args, []string{"/bin/sh", "-c", "less -R"}) {
		t.Fatalf("cmd = %q %q", cmd.Path, cmd.Args)
	}
	if !reflect.DeepEqual(cmd.Env, environ) {
		t.Fatalf("env = %q", cmd.Env)
	}
	if cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil {
		t.Fatal("stdio must be left unset (tea.ExecProcess fills unset ones with the terminal)")
	}
}

func TestCommandHonoursShellSyntaxInUserPager(t *testing.T) {
	out := filepath.Join(t.TempDir(), "paged out")
	environ := []string{"PATH=/usr/bin:/bin", "OUT=" + out, `AGENTCTL_PAGER=cat > "$OUT"`}
	cmd := Command(lookup(environ...), environ)
	cmd.Stdin = strings.NewReader("diff --git a/x b/x\n+line\n")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "diff --git a/x b/x\n+line\n" {
		t.Fatalf("pager received %q %v", got, err)
	}
}

func TestIsTerminal(t *testing.T) {
	if IsTerminal(&bytes.Buffer{}) {
		t.Error("buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("regular file is not a terminal")
	}
	if IsTerminal(nil) {
		t.Error("nil is not a terminal")
	}
}
