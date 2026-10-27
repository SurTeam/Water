package gosupervisor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestClientCloseKillsSignalIgnoringShell(t *testing.T) {
	server, socket := startSupervisor(t)
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	id := uuid.New()
	ready := filepath.Join(t.TempDir(), "ready")
	_, _, err = client.Spawn(SpawnSpec{ID: id, Program: "/bin/sh", Args: []string{"-c", `trap '' HUP TERM; echo ready > "$1"; while :; do sleep 1; done`, "sh", ready}, Env: os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	server.mu.Lock()
	term := server.terms[id.String()]
	server.mu.Unlock()
	if term == nil {
		t.Fatal("terminal not registered")
	}
	if err := client.CloseTerminal(id); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for !term.isExited() {
		if time.Now().After(deadline) {
			t.Fatal("shell survived terminal.close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
