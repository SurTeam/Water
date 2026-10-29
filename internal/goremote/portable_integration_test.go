package goremote

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

// The runner supplies an owned socket and a freshly built remote server. This
// never touches an installed Water endpoint or shuts down an existing instance.
func TestNativeSSHPortableRoundTrip(t *testing.T) {
	destination := os.Getenv("WATER_SSH_NATIVE_TEST_DESTINATION")
	if destination == "" {
		t.Skip("requires an explicitly owned native SSH fixture")
	}
	if os.Getenv("WATER_REMOTE_CONTROL_SOCKET") == "" {
		t.Fatal("isolated remote endpoint required")
	}

	tunnel, err := Connect(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer tunnel.Close()
	if tunnel.portable == nil {
		t.Fatal("native fixture did not use portable transport")
	}
	defer tunnel.Client().Call("server.shutdown", map[string]any{}, nil)
	session, err := tunnel.Client().OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var spawned struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	program, args := "cmd.exe", []string{"/d", "/c", "echo NATIVE_SSH_ROUNDTRIP"}
	if tunnel.portable.target.OS != "windows" {
		program = "/bin/sh"
		args = []string{"-c", "printf 'NATIVE_SSH_ROUNDTRIP\\n'"}
	}
	if err := session.Dispatch(map[string]any{"type": "terminal.spawn", "program": program, "args": args, "columns": 80, "lines": 24}, &spawned); err != nil {
		t.Fatal(err)
	}
	var exited struct {
		Events []goprotocol.WireTerminalEvent `json:"events"`
	}
	if err := session.CallTimeout("terminal.wait_exit", map[string]any{"terminal_id": spawned.TerminalID, "timeout_ms": 5000}, &exited, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, event := range exited.Events {
		if event.Type == "output" {
			data, err := base64.StdEncoding.DecodeString(event.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			output.Write(data)
		}
	}
	if !strings.Contains(output.String(), "NATIVE_SSH_ROUNDTRIP") {
		t.Fatalf("remote output %q", output.String())
	}
	// Multiple simultaneous stream/control connections must remain independent.
	errors := make(chan error, 3)
	for range 3 {
		go func() { _, err := tunnel.Client().Inspect(10 * time.Second); errors <- err }()
	}
	for range 3 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	session.Close()
	remote := tunnel.RemoteSocket()
	replacement, err := AttachExisting(destination, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	tunnel.Close()
	if tunnel.portable.native != nil && replacement.portable.native != tunnel.portable.native {
		t.Fatal("reconnect did not reuse authenticated transport")
	}
	if _, err := replacement.Client().Inspect(10 * time.Second); err != nil {
		t.Fatalf("closing old transport stopped remote server: %v", err)
	}
	if err := replacement.Client().Call("server.shutdown", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := WaitForShutdown(destination, remote, 15*time.Second); err != nil {
		t.Fatal(err)
	}
}
