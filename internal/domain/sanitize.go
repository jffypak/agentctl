package domain

import (
	"strings"
	"unicode/utf8"
)

const (
	esc = 0x1b
	bel = 0x07
)

// SanitizeTerminalText removes everything a terminal could interpret from
// captured pane text or hook-supplied text before it is displayed: CSI
// sequences, OSC strings (including OSC 52 clipboard writes and OSC 8 links),
// DCS/SOS/PM/APC strings, other ESC sequences, 8-bit C1 controls, DEL, and C0
// controls except newline and tab. Invalid UTF-8 becomes U+FFFD. The result is
// idempotent under a second pass.
func SanitizeTerminalText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			b.WriteRune(utf8.RuneError)
			i++
		case r == esc:
			i = skipEscape(s, i+1)
		case r == 0x9b: // C1 CSI
			i = skipCSI(s, i+size)
		case r == 0x9d || r == 0x90 || r == 0x98 || r == 0x9e || r == 0x9f: // C1 OSC/DCS/SOS/PM/APC
			i = skipString(s, i+size)
		case r < 0x20 && r != '\n' && r != '\t', r == 0x7f, r >= 0x80 && r < 0xa0:
			i += size
		default:
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

// skipEscape consumes the sequence after an ESC at s[i-1] and returns the
// index of the first byte after it.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch c := s[i]; {
	case c == '[':
		return skipCSI(s, i+1)
	case c == ']' || c == 'P' || c == 'X' || c == '^' || c == '_':
		return skipString(s, i+1)
	case c >= 0x20 && c <= 0x2f: // nF: intermediates then one final byte
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
			i++
		}
		return i
	case c >= 0x30 && c <= 0x7e: // two-byte escape (ESC 7, ESC c, ESC =, ...)
		return i + 1
	}
	return i // lone ESC: drop it, reprocess the next byte
}

// skipCSI consumes CSI parameter/intermediate bytes and the final byte. A byte
// outside 0x20-0x7e aborts the sequence without consuming it, so an embedded
// ESC or control is processed on its own.
func skipCSI(s string, i int) int {
	for i < len(s) {
		c := s[i]
		switch {
		case c >= 0x40 && c <= 0x7e:
			return i + 1
		case c >= 0x20 && c <= 0x3f:
			i++
		default:
			return i
		}
	}
	return i
}

// skipString consumes an OSC/DCS/SOS/PM/APC body up to and including its
// terminator: BEL, ESC \ (ST), or C1 ST (U+009C). Unterminated strings run to
// the end of the input, which drops the rest — safer than showing it.
func skipString(s string, i int) int {
	for i < len(s) {
		if s[i] == bel {
			return i + 1
		}
		if s[i] == esc && i+1 < len(s) && s[i+1] == '\\' {
			return i + 2
		}
		if r, size := utf8.DecodeRuneInString(s[i:]); r == 0x9c {
			return i + size
		} else {
			i += size
		}
	}
	return i
}
