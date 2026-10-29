package goterminal

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestCloseKillsSignalIgnoringShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	ready := filepath.Join(t.TempDir(), "ready")
	r := NewRegistry()
	defer r.CloseAll()
	term, err := r.Spawn("/bin/sh", []string{"-c", `trap '' HUP TERM; echo ready > "$1"; while :; do sleep 1; done`, "sh", ready}, goprotocol.TerminalSize{Columns: 20, Lines: 4})
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
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.readerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("close retained reader")
	}
	// waitLoop must observe actual child exit, even when HUP and TERM are ignored.
	deadline = time.Now().Add(2 * time.Second)
	for {
		r.mu.Lock()
		_, exited := r.exited[term.ID]
		r.mu.Unlock()
		if exited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell survived close")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
