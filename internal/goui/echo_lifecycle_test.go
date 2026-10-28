package goui

import (
	"runtime"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

// The echo bypass adds per-terminal state (credits, deadline, latency, a
// capacity-one signal). Closing a terminal must release all of it: no
// goroutines, no retained heap growth across repeated create/close cycles.
func TestEchoStateIsReleasedAcrossTerminalLifecycle(t *testing.T) {
	runtime.GC()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	baseGoroutines := runtime.NumGoroutine()
	const cycles = 2000
	for i := 0; i < cycles; i++ {
		emu := govt.New(80, 24, 100)
		term := &terminalClient{id: uuid.New(), emu: emu, echoSignal: make(chan struct{}, 1)}
		term.noteInputEcho(time.Now())
		term.inputPending.Store(true)
		term.close()
	}
	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 8<<20 {
		t.Fatalf("heap grew by %d bytes across %d terminal lifecycles", grown, cycles)
	}
	if goroutines := runtime.NumGoroutine(); goroutines > baseGoroutines+4 {
		t.Fatalf("goroutines grew from %d to %d", baseGoroutines, goroutines)
	}
}
