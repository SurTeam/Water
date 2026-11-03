package goserver_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

// A program that never reads stdin must consume only its own bounded input
// queue. Its dedicated connection may reject or drop that input, but another
// pane and the GUI's control session must keep responding.
func TestBlockedPaneInputDoesNotStallServerOrAnotherPane(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	socket := filepath.Join("/tmp", "water-input-isolation-"+uuid.NewString()+".sock")
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := client.CallTimeout("ping", nil, nil, time.Second); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("server did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	gui, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer gui.Close()

	var blocked struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := gui.Dispatch(map[string]any{
		"type":    "terminal.spawn",
		"program": "/bin/sh",
		"args":    []string{"-c", "exec sleep 30"},
		"columns": 80,
		"lines":   24,
	}, &blocked); err != nil {
		t.Fatal(err)
	}
	var split struct {
		PaneID uuid.UUID `json:"pane_id"`
	}
	if err := gui.Dispatch(map[string]any{"type": "pane.split", "direction": "right"}, &split); err != nil {
		t.Fatal(err)
	}
	var quiet struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := gui.Dispatch(map[string]any{
		"type":    "terminal.spawn",
		"pane_id": split.PaneID,
		"program": "/bin/sh",
		"args":    []string{"-c", "while read line; do echo got:$line; done"},
		"columns": 80,
		"lines":   24,
	}, &quiet); err != nil {
		t.Fatal(err)
	}
	if blocked.TerminalID == uuid.Nil || quiet.TerminalID == uuid.Nil {
		t.Fatal("missing spawned terminal")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	blockedSub, err := gui.AttachTerminal(ctx, blocked.TerminalID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blockedSub.Close()
	quietSub, err := gui.AttachTerminal(ctx, quiet.TerminalID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer quietSub.Close()

	// Fill the blocked pane's server-side input queue. The child never reads
	// stdin, so writes eventually fill the PTY and the bounded queue.
	chunk := bytes.Repeat([]byte("x"), 128*1024)
	start := time.Now()
	for i := 0; i < 24; i++ {
		if err := gui.SendInput(blocked.TerminalID, chunk); err != nil {
			break
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("blocked pane input blocked the caller for %s", elapsed)
	}

	if err := gui.SendInput(quiet.TerminalID, []byte("hello\n")); err != nil {
		t.Fatalf("quiet pane input: %v", err)
	}
	waitSubText(t, quietSub, "got:hello")

	if err := gui.CallTimeout("ping", nil, nil, 2*time.Second); err != nil {
		t.Fatalf("blocked pane input starved GUI control: %v", err)
	}
	var state struct {
		Workspaces []any `json:"workspaces"`
	}
	if err := gui.CallTimeout("state.dump", nil, &state, 2*time.Second); err != nil {
		t.Fatalf("blocked pane input starved state dump: %v", err)
	}
}

func waitSubText(t *testing.T, sub *goclient.TerminalSubscription, want string) {
	t.Helper()
	var output []byte
	deadline := time.After(3 * time.Second)
	for !strings.Contains(string(output), want) {
		select {
		case event := <-sub.Events:
			output = append(output, event.Event.Data...)
		case <-deadline:
			t.Fatalf("terminal did not produce %q; output=%q", want, output)
		}
	}
}
