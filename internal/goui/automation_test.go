package goui

import (
	"gioui.org/io/key"
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestAutomationUsesTerminalInput(t *testing.T) {
	for _, tc := range []struct {
		spec        string
		application bool
		want        string
	}{
		{"text:echo WATER_Go_中文\n", false, "echo WATER_Go_中文\n"},
		{"A", false, "A"},
		{"enter", false, "\r"},
		{"up", false, "\x1b[A"},
		{"up", true, "\x1bOA"},
		{"ctrl-left", true, "\x1b[1;5D"},
		{"shift-tab", false, "\x1b[Z"},
		{"ctrl-l", false, "\x0c"},
		{"f5", false, "\x1b[15~"},
		{"page-up", false, "\x1b[5~"},
		{"ctrl-page-down", false, "\x1b[6;5~"},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			var got string
			terminal := &TerminalInput{OnInput: func(data []byte) { got += string(data) }}
			if !routeAutomationInput(terminal, govt.Snapshot{Cols: 80, Rows: 24, ApplicationCursor: tc.application}, tc.spec) {
				t.Fatal("input was rejected")
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAutomationRejectsUnknownKeys(t *testing.T) {
	for _, spec := range []string{"", "unknown", "meta-up", "ctrl-unknown"} {
		if _, ok := automationInputEvent(spec); ok {
			t.Fatalf("accepted %q", spec)
		}
	}
}

func TestWindowShortcutConsumesInput(t *testing.T) {
	var got string
	called := 0
	terminal := &TerminalInput{
		OnInput: func(data []byte) { got += string(data) },
		OnShortcut: func(ev key.Event) bool {
			if ev.Name == "T" && ev.Modifiers == key.ModCommand {
				called++
				return true
			}
			return false
		},
	}
	snapshot := govt.Snapshot{Cols: 80, Rows: 24}
	if !routeAutomationInput(terminal, snapshot, "cmd-t") {
		t.Fatal("shortcut rejected")
	}
	if called != 1 || got != "" {
		t.Fatalf("shortcut calls=%d, PTY input=%q", called, got)
	}
	if !routeAutomationInput(terminal, snapshot, "ctrl-l") {
		t.Fatal("terminal key rejected")
	}
	if got != "\x0c" {
		t.Fatalf("unmatched shortcut swallowed terminal input: %q", got)
	}
}
