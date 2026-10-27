package goterminal

import (
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestCloseUnblocksPublisherWithFullSubscriber(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{"-c", "exec sleep 30"}, goprotocol.TerminalSize{Columns: 20, Lines: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()
	events, _, cancel := term.Subscribe()
	defer cancel()
	for i := 0; i < cap(events); i++ {
		term.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Data: []byte("queued")})
	}
	done := make(chan struct{})
	go func() {
		term.publish(goprotocol.TerminalEvent{Kind: goprotocol.OutputEvent, Data: []byte("blocked")})
		close(done)
	}()
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
