package goterminal

import (
	"context"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestForegroundNameFollowsPTYJobAndReturnsToShell(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{"-i"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()
	waitName := func(name string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if got := r.ForegroundNames(context.Background())[term.ID]; got == name {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("foreground never became %s", name)
	}
	waitName("sh")
	if err := term.Write([]byte("sleep 30\n")); err != nil {
		t.Fatal(err)
	}
	waitName("sleep")
	if err := term.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	waitName("sh")
}
