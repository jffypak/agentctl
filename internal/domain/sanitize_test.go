package domain

import (
	"strings"
	"testing"
	"unicode"
)

func TestSanitizeTerminalText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"keeps newline and tab", "a\tb\nc", "a\tb\nc"},
		{"keeps unicode", "héllo · 世界 ✓", "héllo · 世界 ✓"},
		{"CSI color", "\x1b[31mred\x1b[0m", "red"},
		{"CSI cursor move", "a\x1b[2;5Hb", "ab"},
		{"CSI private mode", "\x1b[?2004hprompt\x1b[?2004l", "prompt"},
		{"CSI clear screen", "x\x1b[2Jy", "xy"},
		{"CSI with intermediates", "a\x1b[1 qb", "ab"},
		{"unterminated CSI dropped", "a\x1b[31", "a"},
		{"OSC title BEL", "\x1b]0;evil title\x07text", "text"},
		{"OSC title ST", "\x1b]2;evil\x1b\\text", "text"},
		{"OSC 52 clipboard BEL", "pre\x1b]52;c;ZWNobyBwd25lZA==\x07post", "prepost"},
		{"OSC 52 clipboard ST", "pre\x1b]52;c;ZWNobyBwd25lZA==\x1b\\post", "prepost"},
		{"OSC 8 hyperlink", "\x1b]8;;https://evil\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"unterminated OSC dropped", "a\x1b]52;c;AAAA", "a"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", "ab"},
		{"APC", "a\x1b_payload\x1b\\b", "ab"},
		{"PM and SOS", "a\x1b^pm\x1b\\b\x1bXsos\x1b\\c", "abc"},
		{"two-byte escapes", "a\x1b7b\x1b8c\x1bcd\x1b=e", "abcde"},
		{"charset designation", "a\x1b(Bb", "ab"},
		{"lone trailing ESC", "abc\x1b", "abc"},
		{"C0 controls stripped", "a\x00b\x01c\x07d\x08e\x0bf\x0cg\rh\x1ai\x7fj", "abcdefghij"},
		{"carriage return stripped", "line1\r\nline2\r\n", "line1\nline2\n"},
		{"C1 CSI (U+009B)", "a\u009b31mb", "ab"},
		{"C1 OSC (U+009D) with C1 ST", "a\u009d52;c;QUFB\u009cb", "ab"},
		{"C1 OSC (U+009D) with BEL", "a\u009d0;t\x07b", "ab"},
		{"other C1 dropped", "a\u0085b\u0084c\u009cd", "abcd"},
		{"C1 DCS swallows to ST", "a\u0090q#0\u009cb", "ab"},
		{"invalid utf8 replaced", "a\xffb\x9bc", "a�b�c"},
		{"nested-looking", "\x1b[\x1b]52;c;X\x07m", "m"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		got := SanitizeTerminalText(c.in)
		if got != c.want {
			t.Errorf("%s: SanitizeTerminalText(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestSanitizeOutputHasNoControls(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 256; i++ {
		b.WriteByte(byte(i))
		b.WriteString("\x1b[")
		b.WriteByte(byte(i))
		b.WriteString("\x1b]")
		b.WriteByte(byte(i))
	}
	for r := rune(0x80); r < 0xa0; r++ {
		b.WriteRune(r)
		b.WriteString("x")
	}
	out := SanitizeTerminalText(b.String())
	for _, r := range out {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			t.Fatalf("output contains control rune %U: %q", r, out)
		}
	}
}

func FuzzSanitizeTerminalText(f *testing.F) {
	f.Add("\x1b]52;c;AAAA\x07hi\x1b[31m")
	f.Add("\u009b1m\u009d0;x\u009c")
	f.Fuzz(func(t *testing.T, s string) {
		out := SanitizeTerminalText(s)
		for _, r := range out {
			if r != '\n' && r != '\t' && unicode.IsControl(r) {
				t.Fatalf("control rune %U survived in %q", r, out)
			}
		}
		if SanitizeTerminalText(out) != out {
			t.Fatalf("not idempotent: %q", out)
		}
	})
}
