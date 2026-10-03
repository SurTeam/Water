package xterm

import "testing"

func TestEmojiWidthPreservesCombiningAndTerminalSymbols(t *testing.T) {
	u := NewUnicodeService()
	for _, tc := range []struct {
		r     rune
		width int
	}{
		{'🍺', 2}, {'😀', 2}, {'🚀', 2}, {'界', 2},
		{'█', 1}, {'▏', 1}, {'→', 1},
		{'\ue73c', 1}, {'\uf120', 1}, {'\U000f0001', 1}, {'\U00100000', 1},
		{'\u3099', 0}, {'\u0301', 0}, {'\ufe0f', 0}, {'\U000e0100', 0},
	} {
		if got := u.Wcwidth(tc.r); got != tc.width {
			t.Fatalf("U+%04X: width %d, want %d", tc.r, got, tc.width)
		}
	}
}
