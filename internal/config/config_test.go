package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agentctl", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMissingFileIsTheZeroConfig(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"", filepath.Join(t.TempDir(), "nope", "config.yaml")} {
		c, err := Load(p)
		if err != nil || c.Keys != nil {
			t.Fatalf("Load(%q) = %+v, %v", p, c, err)
		}
	}
}

func TestLoadEmptyOrCommentOnlyFile(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "# nothing yet\n", "keys:\n"} {
		c, err := Load(write(t, content))
		if err != nil || len(c.Keys) != 0 {
			t.Fatalf("Load(%q) = %+v, %v", content, c, err)
		}
	}
}

func TestLoadKeysAcceptsAScalarOrAList(t *testing.T) {
	t.Parallel()
	c, err := Load(write(t, `# my keys
keys:
  diff: D
  logs: [l, L]
  up:
    - k
    - up
  confirm: y      # YAML 1.1 would read y as a bool; it is a key here
  filter: "/"
  send: ctrl+s
  new: 1
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"diff": {"D"}, "logs": {"l", "L"}, "up": {"k", "up"}, "confirm": {"y"},
		"filter": {"/"}, "send": {"ctrl+s"}, "new": {"1"},
	}
	if !reflect.DeepEqual(c.Keys, want) {
		t.Fatalf("keys = %#v", c.Keys)
	}
}

func TestLoadEmptyBindingIsKeptForTheCallerToReject(t *testing.T) {
	t.Parallel()
	c, err := Load(write(t, "keys:\n  diff:\n  logs: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Keys["diff"]; !ok || len(got) != 0 {
		t.Fatalf("diff = %#v (present %v)", got, ok)
	}
	if got, ok := c.Keys["logs"]; !ok || len(got) != 0 {
		t.Fatalf("logs = %#v (present %v)", got, ok)
	}
}

func TestLoadRejectsMalformedFilesOnOneLine(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"syntax":          "keys:\n  diff: [d\n",
		"tabs":            "keys:\n\tdiff: d\n",
		"unknown setting": "theme: dark\nkeys:\n  diff: d\n",
		"keys not a map":  "keys: [d, l]\n",
		"nested value":    "keys:\n  diff: {a: b}\n",
		"nested list":     "keys:\n  diff: [[d]]\n",
		"not a mapping":   "- keys\n",
		"two documents":   "keys:\n  diff: d\n---\nkeys:\n  logs: l\n",
	}
	for name, content := range cases {
		_, err := Load(write(t, content))
		if err == nil {
			t.Errorf("%s: accepted %q", name, content)
			continue
		}
		if strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: error spans lines: %q", name, err)
		}
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: error %v is not ErrMalformed", name, err)
		}
	}
}

func TestLoadUnreadableFileIsAnError(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || errors.Is(err, ErrMalformed) {
		t.Fatalf("Load(directory) = %v", err)
	}
}

func TestPathFollowsXDGConfigHome(t *testing.T) {
	t.Parallel()
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"XDG_CONFIG_HOME": "/c", "HOME": "/u"}, "/c/agentctl/config.yaml"},
		{map[string]string{"XDG_CONFIG_HOME": "relative", "HOME": "/u"}, "/u/.config/agentctl/config.yaml"},
		{map[string]string{"HOME": "/u", "AGENTCTL_HOME": "/h"}, "/u/.config/agentctl/config.yaml"},
		{map[string]string{"AGENTCTL_HOME": "/h"}, ""}, // no HOME and no XDG: no config file
		{nil, ""},
	}
	for _, c := range cases {
		if got := Path(env(c.env)); got != c.want {
			t.Errorf("Path(%v) = %q, want %q", c.env, got, c.want)
		}
	}
}
