package goterminal

import (
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/creack/pty"
	"testing"
)

func TestResizePublishesPixelGeometryToPTY(t *testing.T) {
	r := NewRegistry()
	defer r.CloseAll()
	term, err := r.Spawn("/bin/sh", []string{"-c", "read hold"}, goprotocol.TerminalSize{Columns: 80, Lines: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := term.ResizeWithCells(goprotocol.TerminalSize{Columns: 90, Lines: 26}, 16, 44); err != nil {
		t.Fatal(err)
	}
	ws, err := pty.GetsizeFull(term.ptmx)
	if err != nil {
		t.Fatal(err)
	}
	if ws.X != 1440 || ws.Y != 1144 || ws.Cols != 90 || ws.Rows != 26 {
		t.Fatalf("PTY geometry %+v", ws)
	}
	if err := term.Resize(goprotocol.TerminalSize{Columns: 80, Lines: 24}); err != nil {
		t.Fatal(err)
	}
	ws, err = pty.GetsizeFull(term.ptmx)
	if err != nil || ws.X != 1280 || ws.Y != 1056 {
		t.Fatalf("grid-only resize lost cells: %+v %v", ws, err)
	}
}
