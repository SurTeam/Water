package govt

import "testing"

func TestReadContentCursorTracksRetainedRows(t *testing.T) {
	e := New(20, 2, 3)
	defer e.Close()
	before := e.ReadContentCursor()
	if before.Row != 0 || before.AltScreen {
		t.Fatalf("initial %#v", before)
	}
	e.WriteReplay([]byte("one\r\ntwo\r\nthree\r\nfour\r\nfive\r\nsix"))
	after := e.ReadContentCursor()
	if after.TrimmedLines+uint64(after.Row) != 5 {
		t.Fatalf("cursor lost absolute row: %#v", after)
	}
	e.WriteReplay([]byte("\x1b[?1049h\x1b[2;1H"))
	if alt := e.ReadContentCursor(); !alt.AltScreen || alt.Row != 1 {
		t.Fatalf("alternate cursor %#v", alt)
	}
}
