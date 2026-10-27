package goui

import (
	"sync"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/govt"
)

func TestSnapshotConstructionDoesNotBlockViewOrLoseConcurrentInput(t *testing.T) {
	emu := govt.New(20, 4, 100)
	emu.Write([]byte("before"))
	term := &terminalClient{emu: emu, snapshot: emu.FrameSnapshot(), selection: Selection{Active: true}}
	term.inputPending.Store(true)
	term.viewStale.Store(true)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var resume sync.Once
	unblock := func() { resume.Do(func() { close(release) }) }
	await := func(ch <-chan struct{}, message string) bool {
		t.Helper()
		select {
		case <-ch:
			return true
		case <-time.After(time.Second):
			t.Error(message)
			return false
		}
	}
	go func() {
		defer close(done)
		term.parseMu.Lock()
		defer term.parseMu.Unlock()
		term.publishSnapshotWith(func(e *govt.Emulator) govt.Snapshot {
			close(started)
			<-release
			return e.FrameSnapshot()
		})
	}()
	defer func() {
		unblock()
		// Never close the emulator while a failed publisher may still use it.
		if await(done, "publisher did not exit") {
			emu.Close()
		}
	}()
	if !await(started, "snapshot construction did not start") {
		return
	}
	// Draw and hit testing must be able to read the previous immutable frame.
	if !term.mu.TryRLock() {
		t.Fatal("GUI read blocked on snapshot construction")
	}
	old := term.snapshot
	term.mu.RUnlock()
	if old.Cols != 20 {
		t.Fatal("previous frame unavailable during construction")
	}
	// Selection move/end also need only the view lock.
	if !term.mu.TryLock() {
		t.Fatal("selection update blocked on snapshot construction")
	}
	cleared := !term.selection.Active
	selection := Selection{Active: true, FocusCol: 7, FocusRow: 2}
	term.selection = selection
	term.mu.Unlock()
	if !cleared {
		t.Fatal("preexisting input did not clear selection")
	}
	term.inputPending.Store(true)
	if term.parseMu.TryLock() {
		term.parseMu.Unlock()
		t.Fatal("snapshot construction lost viewport/lifetime ordering")
	}
	unblock()
	if !await(done, "snapshot publication did not finish") {
		return
	}
	term.mu.RLock()
	defer term.mu.RUnlock()
	if term.selection != selection {
		t.Fatal("publication overwrote concurrent selection")
	}
	if !term.inputPending.Load() {
		t.Fatal("publication consumed input arriving during construction")
	}
	if term.viewStale.Load() {
		t.Fatal("published view remained stale")
	}
	if term.snapshot.RowsData[0].Cells[0].Text != "b" {
		t.Fatal("new frame was not published")
	}
}
