package main

import "strings"

// tokenKind classifies terminal input.
type tokenKind int

const (
	tokText      tokenKind = iota // printable text (typed)
	tokEnter                      // CR or LF outside a paste
	tokBackspace                  // DEL or BS
	tokPaste                      // a complete bracketed paste (ESC[200~ ... ESC[201~)
	tokInterrupt                  // ctrl-c (raw mode: no SIGINT)
	tokEOF                        // ctrl-d
)

type token struct {
	kind tokenKind
	text string
}

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// tokenizer turns raw tty bytes into tokens. It is fed arbitrary chunks (a
// paste or an escape sequence may be split across reads) and keeps whatever
// is incomplete for the next Feed.
type tokenizer struct {
	pending  []byte
	inPaste  bool
	paste    strings.Builder
	lastCR   bool
	textBuf  strings.Builder
	finished []token
}

func (t *tokenizer) emitText() {
	if t.textBuf.Len() > 0 {
		t.finished = append(t.finished, token{kind: tokText, text: t.textBuf.String()})
		t.textBuf.Reset()
	}
}

func (t *tokenizer) emit(k tokenKind) {
	t.emitText()
	t.finished = append(t.finished, token{kind: k})
}

// Feed consumes b and returns the tokens completed so far.
func (t *tokenizer) Feed(b []byte) []token {
	buf := append(t.pending, b...)
	t.pending = nil
	i := 0
	for i < len(buf) {
		if t.inPaste {
			end := strings.Index(string(buf[i:]), pasteEnd)
			if end < 0 {
				// keep a possible partial end marker for the next chunk
				keep := partialSuffix(buf[i:], pasteEnd)
				t.paste.Write(buf[i : len(buf)-keep])
				t.pending = append([]byte(nil), buf[len(buf)-keep:]...)
				return t.flush()
			}
			t.paste.Write(buf[i : i+end])
			i += end + len(pasteEnd)
			t.inPaste = false
			t.emitText()
			t.finished = append(t.finished, token{kind: tokPaste, text: normalizeNewlines(t.paste.String())})
			t.paste.Reset()
			continue
		}
		c := buf[i]
		if c == 0x1b {
			n, complete := escapeLen(buf[i:])
			if !complete {
				t.pending = append([]byte(nil), buf[i:]...)
				return t.flush()
			}
			if string(buf[i:i+n]) == pasteStart {
				t.emitText()
				t.inPaste = true
			}
			i += n // other sequences (arrows, focus events, ...) are ignored
			t.lastCR = false
			continue
		}
		switch {
		case c == '\r':
			t.emit(tokEnter)
		case c == '\n':
			if !t.lastCR {
				t.emit(tokEnter)
			}
		case c == 0x7f || c == 0x08:
			t.emit(tokBackspace)
		case c == 0x03:
			t.emit(tokInterrupt)
		case c == 0x04:
			t.emit(tokEOF)
		case c < 0x20:
			// other control bytes are ignored
		default:
			t.textBuf.WriteByte(c)
		}
		t.lastCR = c == '\r'
		i++
	}
	return t.flush()
}

func (t *tokenizer) flush() []token {
	t.emitText()
	out := t.finished
	t.finished = nil
	return out
}

// escapeLen returns the length of the escape sequence at b[0] == ESC, and
// whether it is complete. CSI: ESC [ params final(0x40-0x7e); otherwise ESC
// plus one byte.
func escapeLen(b []byte) (int, bool) {
	if len(b) < 2 {
		return 0, false
	}
	if b[1] != '[' {
		return 2, true
	}
	for j := 2; j < len(b); j++ {
		if b[j] >= 0x40 && b[j] <= 0x7e {
			return j + 1, true
		}
	}
	return 0, false
}

// partialSuffix is the length of the longest suffix of b that is a proper
// prefix of marker.
func partialSuffix(b []byte, marker string) int {
	for n := len(marker) - 1; n > 0; n-- {
		if len(b) >= n && string(b[len(b)-n:]) == marker[:n] {
			return n
		}
	}
	return 0
}

// normalizeNewlines maps CRLF and CR to LF: tmux paste-buffer replaces LF
// with CR, and a multi-line body must arrive as the lines it was.
func normalizeNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}
