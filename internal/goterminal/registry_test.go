package goterminal

import (
	"bytes"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestExitIsSequencedAfterFinalOutput(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{"-c", "printf final-output"}, goprotocol.TerminalSize{Columns:80, Lines:24})
	if err != nil { t.Fatal(err) }
	defer r.CloseAll()

	events, done, cancel := term.Subscribe()
	defer cancel()
	deadline := time.NewTimer(2*time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Kind == goprotocol.ExitEvent {
				replay := term.Replay()
				if len(replay)==0 || replay[len(replay)-1].Kind != goprotocol.ExitEvent {
					t.Fatalf("exit is not last event: %#v", replay)
				}
				var out []byte
				for _, item := range replay {
					if item.Kind == goprotocol.OutputEvent { out=append(out,item.Data...) }
				}
				if !bytes.Contains(out,[]byte("final-output")) {
					t.Fatalf("missing final output: %q",out)
				}
				return
			}
		case <-done:
			t.Fatal("subscription ended before exit")
		case <-deadline.C:
			t.Fatal("timed out waiting for exit")
		}
	}
}
