package goterminal

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestResizeTimeoutCancelsQueuedRequest(t *testing.T) {
	term := &Terminal{
		resizeRequests: make(chan resizeRequest, 1),
		readerDone:     make(chan struct{}),
		closed:         make(chan struct{}),
	}
	if err := term.Resize(goprotocol.TerminalSize{Columns: 40, Lines: 8}); !errors.Is(err, ErrResizeTimeout) {
		t.Fatalf("resize error = %v", err)
	}
	request := <-term.resizeRequests
	if !request.canceled.Load() {
		t.Fatal("timed-out resize remained eligible for application")
	}
}

func TestFullSubscriberDoesNotBlockAnotherPaneOrClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix shell/process fixture; native Windows lifecycle is covered separately")
	}
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{"-c", "exec sleep 30"}, goprotocol.TerminalSize{Columns: 20, Lines: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()
	_, _, cancel := term.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberQueueEvents*3; i++ {
			term.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Data: []byte("queued")})
		}
		term.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Data: []byte("blocked")})
		close(done)
	}()
	other, err := r.Spawn("/bin/sh", []string{"-c", "exec sleep 30"}, goprotocol.TerminalSize{Columns: 20, Lines: 4})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Resize(goprotocol.TerminalSize{Columns: 40, Lines: 8}); err != nil {
		t.Fatalf("slow pane blocked neighboring resize: %v", err)
	}
	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closed terminal retained blocked publisher")
	}
	select {
	case <-term.readerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("closed terminal retained PTY reader")
	}
}
