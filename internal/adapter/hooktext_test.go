package adapter_test

import (
	"testing"

	"github.com/jffypak/agentctl/internal/adapter"
)

func TestHookLine(t *testing.T) {
	cases := map[string]string{
		"a\n b\t\tc":           "a b c",
		"  x  ":                "x",
		"\x1b[31mred\x1b[0m":   "red",
		"\x1b]52;c;Zm9v\x07ok": "ok",
		"":                     "",
	}
	for in, want := range cases {
		if got := adapter.HookLine(in); got != want {
			t.Errorf("HookLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBound(t *testing.T) {
	if got := adapter.Bound("abcdef", 3); got != "abc" {
		t.Errorf("Bound ascii = %q", got)
	}
	if got := adapter.Bound("héllo", 2); got != "h" { // é is 2 bytes: never split a rune
		t.Errorf("Bound rune = %q", got)
	}
	if got := adapter.Bound("ab", 5); got != "ab" {
		t.Errorf("Bound short = %q", got)
	}
}
