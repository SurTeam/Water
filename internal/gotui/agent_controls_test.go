package gotui

import "testing"

func TestAgentControlShortcutsRemoved(t *testing.T) {
	for _, key := range []rune{'i', 'e', 'k'} {
		t.Run(string(key), func(t *testing.T) {
			a := newApp(nil, nil, 100)
			if a.action(key) || a.promptKind != "" || len(a.commands) != 0 {
				t.Fatal("removed Agent shortcut still performs an action")
			}
			if a.status != "Unknown command; Ctrl-B ? opens menu" {
				t.Fatalf("removed shortcut was not rejected: %q", a.status)
			}
		})
	}
}
