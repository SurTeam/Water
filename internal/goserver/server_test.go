package goserver_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func TestServerClientTerminalRoundTrip(t *testing.T) {
	socket := filepath.Join(os.TempDir(), "water-go-test-"+uuid.New().String()+".sock")
	defer os.Remove(socket)
	srv := goserver.New(socket)
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	defer func() {
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("server did not stop")
		}
	}()

	client := goclient.New(socket)
	deadline := time.Now().Add(2 * time.Second)
	for {
		var pong map[string]any
		if err := client.Call("ping", map[string]any{}, &pong); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var spawned struct {
		Type       string    `json:"type"`
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := client.Dispatch(map[string]any{
		"type":    "terminal.spawn",
		"program": "/bin/sh",
		"args":    []string{"-c", "printf 'hello-water\\n'"},
		"columns": 80,
		"lines":   24,
	}, &spawned); err != nil {
		t.Fatal(err)
	}
	if spawned.TerminalID == uuid.Nil {
		t.Fatal("missing terminal id")
	}

	var exited struct {
		TerminalID uuid.UUID                       `json:"terminal_id"`
		Process    any                             `json:"process"`
		Events     []goprotocol.WireTerminalEvent  `json:"events"`
	}
	if err := client.Call("terminal.wait_exit", map[string]any{
		"terminal_id": spawned.TerminalID,
		"timeout_ms":  2000,
	}, &exited); err != nil {
		t.Fatal(err)
	}

	var output strings.Builder
	for _, event := range exited.Events {
		if event.Type != "output" {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(event.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(data)
	}
	if !strings.Contains(output.String(), "hello-water") {
		t.Fatalf("missing terminal output: %q", output.String())
	}

	var state struct {
		StateRevision uint64 `json:"state_revision"`
		Workspaces []any `json:"workspaces"`
	}
	if err := client.Call("state.dump", map[string]any{}, &state); err != nil {
		t.Fatal(err)
	}
	if state.StateRevision == 0 || len(state.Workspaces) == 0 {
		t.Fatalf("unexpected model state: %#v", state)
	}
}
