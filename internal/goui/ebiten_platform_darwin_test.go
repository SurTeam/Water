package goui

import "testing"

func TestNativeMenuUsesConfiguredWindowShortcuts(t *testing.T) {
	for _, tc := range []struct {
		binding, name string
		mask          uintptr
	}{
		{"cmd-w", "w", 1 << 20}, {"cmd-alt-w", "w", 1<<20 | 1<<19}, {"ctrl-shift-m", "m", 1<<18 | 1<<17},
		{"", "", 0}, {"cmd-left", "", 0},
	} {
		name, mask := macMenuEquivalent(tc.binding)
		if name != tc.name || mask != tc.mask {
			t.Fatalf("%s: got %q/%d", tc.binding, name, mask)
		}
	}
}
