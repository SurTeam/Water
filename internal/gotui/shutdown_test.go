package gotui

import "testing"

func TestShutdownPrefixRequiresConfirmation(t *testing.T) {
	a := newApp(nil, nil, 100)
	if a.action('Q') {
		t.Fatal("shutdown exited before confirmation")
	}
	a.prompt = "no"
	a.submitPrompt()
	if a.shutdownPending || len(a.commands) != 0 {
		t.Fatal("cancelled shutdown queued a command")
	}
	a.action('Q')
	a.prompt = "yes"
	a.submitPrompt()
	if !a.shutdownPending {
		t.Fatal("confirmed shutdown not pending")
	}
	if c := <-a.commands; c["type"] != "tui.shutdown" {
		t.Fatalf("unexpected command: %v", c)
	}
	if !a.action('q') {
		t.Fatal("detach shortcut changed")
	}
}
