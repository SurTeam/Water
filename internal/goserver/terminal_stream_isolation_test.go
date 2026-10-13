package goserver_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

// A busy TUI may completely fill the GUI's bounded terminal queue while the
// GUI bootstraps other panes. Neither control RPC nor another terminal attach
// may depend on draining that queue first.
func TestFloodedTUIStreamDoesNotBlockGUIControlOrSecondAttach(t *testing.T) {
	socket := filepath.Join("/tmp", "water-stream-isolation-"+uuid.NewString()+".sock")
	server := goserver.New(socket)
	served := make(chan error, 1)
	go func() { served <- server.ListenAndServe() }()
	t.Cleanup(func() {
		_ = server.Close()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server failed to stop")
		}
		_ = os.Remove(socket + ".lock")
	})

	client := goclient.New(socket)
	startupDeadline := time.Now().Add(2 * time.Second)
	for {
		if err := client.CallTimeout("ping", nil, nil, time.Second); err == nil {
			break
		} else if time.Now().After(startupDeadline) {
			t.Fatalf("server did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	gui, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer gui.Close()

	var busy struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	// Deliberately produces more than 64 live events and never waits for UI
	// painting, as an alternate-screen TUI typically would.
	if err := gui.Dispatch(map[string]any{
		"type": "terminal.spawn",
		"program": "/bin/sh",
		"args": []string{"-c", "while :; do printf '0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ\\n'; done"},
		"columns": 80,
		"lines": 24,
	}, &busy); err != nil {
		t.Fatal(err)
	}
	if busy.TerminalID == uuid.Nil {
		t.Fatal("missing busy terminal")
	}
	var first struct {
		LastSeq uint64 `json:"last_seq"`
	}
	if err := gui.Attach(busy.TerminalID, &first); err != nil {
		t.Fatalf("attach busy TUI: %v", err)
	}
	defer gui.Detach(busy.TerminalID)

	// No consumer: model a bootstrap paused while it reconstructs other panes.
	// Prior to the fix, this blocked the GUI session read loop and timed out
	// state.dump, ping, and all subsequent terminal.attach requests.
	deadline := time.Now().Add(5 * time.Second)
	for len(gui.Events) < cap(gui.Events) {
		if time.Now().After(deadline) {
			t.Fatalf("TUI did not saturate GUI event queue: %d/%d", len(gui.Events), cap(gui.Events))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := gui.CallTimeout("ping", nil, nil, 2*time.Second); err != nil {
		t.Fatalf("busy TUI starved GUI RPC: %v", err)
	}

	var second struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := gui.Dispatch(map[string]any{
		"type": "terminal.spawn",
		"program": "/bin/sh",
		"args": []string{"-c", "printf 'quiet-terminal\\n'"},
		"columns": 80,
		"lines": 24,
	}, &second); err != nil {
		t.Fatalf("busy TUI blocked another terminal spawn: %v", err)
	}
	if second.TerminalID == uuid.Nil {
		t.Fatal("missing second terminal")
	}
	var other struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := gui.Attach(second.TerminalID, &other); err != nil {
		t.Fatalf("busy TUI blocked attaching a different terminal: %v", err)
	}
	defer gui.Detach(second.TerminalID)
	if other.TerminalID != second.TerminalID {
		t.Fatal(fmt.Sprintf("unexpected attached terminal %s, want %s", other.TerminalID, second.TerminalID))
	}

	var info goprotocol.ServerInfo
	if err := gui.CallTimeout("server.inspect", nil, &info, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if info.UISessions != 1 {
		t.Fatalf("data-only connections counted as GUI windows: %d", info.UISessions)
	}
}
