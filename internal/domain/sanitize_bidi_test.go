package domain

import "testing"

// Bidi embedding/override/isolate controls (the "Trojan Source" set,
// CVE-2021-42574) and directional marks let pane output or hook text reorder
// what the user sees. They must not survive sanitization.
func TestSanitizeTerminalTextStripsBidiControls(t *testing.T) {
	bidi := []rune{
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // LRE RLE PDF LRO RLO
		0x2066, 0x2067, 0x2068, 0x2069, // LRI RLI FSI PDI
		0x200E, 0x200F, 0x061C, // LRM RLM ALM
	}
	for _, r := range bidi {
		in := "ok" + string(r) + "done"
		if got := SanitizeTerminalText(in); got != "okdone" {
			t.Errorf("U+%04X: got %q, want %q", r, got, "okdone")
		}
	}
}

func TestSanitizeTerminalTextKeepsOrdinaryRTLText(t *testing.T) {
	in := "שלום مرحبا 你好 café\n\tend"
	if got := SanitizeTerminalText(in); got != in {
		t.Fatalf("got %q, want unchanged %q", got, in)
	}
}
