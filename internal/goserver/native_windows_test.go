package goserver

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/gosupervisor"
	"github.com/google/uuid"
)

func TestWindowsDefaultShellRoundTrip(t *testing.T) {
	t.Setenv("WATER_SHELL", "")
	server := NewWithConfig(filepath.Join(t.TempDir(), "s.sock"), goconfig.Default())
	defer server.Close()
	if err := server.Initialize(true, true); err != nil {
		t.Fatal(err)
	}
	state := server.model.Dump()
	if state.FocusedPane == nil {
		t.Fatal("no focused pane")
	}
	id, ok := server.model.TerminalForPane(*state.FocusedPane)
	if !ok {
		t.Fatal("no initial terminal")
	}
	term, ok := server.registry.Get(id)
	if !ok {
		t.Fatal("terminal unavailable")
	}
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(replayText(term.Replay()), ">") {
		if time.Now().After(deadline) {
			t.Fatalf("shell not ready: %q", replayText(term.Replay()))
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := term.Write([]byte("Write-Output ('NATIVE_'+'ROUNDTRIP')\r")); err != nil {
		t.Fatal(err)
	}
	for !strings.Contains(replayText(term.Replay()), "NATIVE_ROUNDTRIP") {
		if time.Now().After(deadline) {
			t.Fatalf("shell round trip: %q", replayText(term.Replay()))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// The binary path is supplied by the native integration runner; no installed
// Water processes or default endpoints are used.
func TestWindowsDetachedServerLifecycle(t *testing.T) {
	binary := os.Getenv("WATER_WINDOWS_SERVER_BINARY")
	if binary == "" {
		t.Skip("requires newly built standalone server")
	}
	directory := t.TempDir()
	socket := filepath.Join(directory, "s.sock")
	config := filepath.Join(directory, "c.json")
	if err := goconfig.Save(config, goconfig.Default()); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "--daemonize", "--socket", socket, "--config", config, "--empty-workspace")
	command.Env = append(os.Environ(), "WATER_PTY_SUPERVISOR_SOCKET="+filepath.Join(directory, "p.sock"))
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("launcher: %s: %v", out, err)
	}
	client := goclient.New(socket)
	defer client.Call("server.shutdown", map[string]any{}, nil)
	deadline := time.Now().Add(15 * time.Second)
	for {
		if err := client.Call("ping", map[string]any{}, nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detached server did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	supervisor, err := gosupervisor.Dial(filepath.Join(directory, "p.sock"))
	if err != nil {
		t.Fatalf("detached PTY supervisor unavailable: %v", err)
	}
	supervisor.Close()
	var spawned struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := client.Dispatch(map[string]any{"type": "terminal.spawn", "program": "cmd.exe", "args": []string{"/d", "/c", "echo DETACHED_NATIVE"}, "columns": 80, "lines": 24}, &spawned); err != nil {
		t.Fatal(err)
	}
	var exited struct {
		Events []goprotocol.WireTerminalEvent `json:"events"`
	}
	if err := client.Call("terminal.wait_exit", map[string]any{"terminal_id": spawned.TerminalID, "timeout_ms": 5000}, &exited); err != nil {
		t.Fatal(err)
	}
	if len(exited.Events) == 0 {
		t.Fatal("missing supervised terminal events")
	}
	if err := client.Call("server.shutdown", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := os.Lstat(socket)
		if os.IsNotExist(err) {
			if conn, err := gosupervisor.Dial(filepath.Join(directory, "p.sock")); err == nil {
				conn.Close()
				t.Fatal("supervisor remains after owned server shutdown")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned server socket remains after shutdown")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func replayText(events []goprotocol.TerminalEvent) string {
	var text strings.Builder
	for _, event := range events {
		if event.Kind == goprotocol.OutputEvent {
			text.Write(event.Data)
		}
	}
	return text.String()
}
