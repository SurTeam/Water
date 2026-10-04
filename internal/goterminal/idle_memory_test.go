package goterminal

import (
	"runtime"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestIdleTerminalsDoNotReserveReadQueue(t *testing.T) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	r := NewRegistry()
	defer r.CloseAll()
	for i := 0; i < 4; i++ {
		term, err := r.Spawn("/bin/sh", []string{"-c", "read line; printf READY; read line"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
		if err != nil {
			t.Fatal(err)
		}
		events, done, cancel := term.Subscribe()
		if err := term.Write([]byte("start\n")); err != nil {
			t.Fatal(err)
		}
		timer := time.NewTimer(2 * time.Second)
		ready := false
		for !ready {
			select {
			case ev := <-events:
				ready = ev.Kind == goprotocol.OutputEvent
			case <-done:
				t.Fatal("terminal closed")
			case <-timer.C:
				t.Fatal("no PTY output")
			}
		}
		timer.Stop()
		cancel()
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("four idle terminals retain %d bytes", growth)
	if growth > 4*1024*1024 {
		t.Fatalf("idle PTYs reserve their burst queue: %d bytes", growth)
	}
}
