package govt

import (
	"bytes"
	"testing"
)

func TestSnapshotPreservesCellStyle(t *testing.T) {
	e := New(8, 2, 100)
	defer e.Close()
	e.Write([]byte("\x1b[1;3;4;38;2;1;2;3;48;5;17mA\x1b[0m界"))

	s := e.Snapshot()
	if len(s.RowsData) != 2 || len(s.RowsData[0].Cells) != 8 {
		t.Fatalf("unexpected geometry: %#v", s)
	}
	a := s.RowsData[0].Cells[0]
	if a.Text != "A" || !a.Bold || !a.Italic || !a.Underline {
		t.Fatalf("style not preserved: %#v", a)
	}
	if a.FG.Mode != ColorRGB || a.FG.Value != 0x010203 {
		t.Fatalf("foreground not preserved: %#v", a.FG)
	}
	if a.BG.Mode != ColorPalette || a.BG.Value != 17 {
		t.Fatalf("background not preserved: %#v", a.BG)
	}
	wide := s.RowsData[0].Cells[1]
	if wide.Text != "界" || wide.Width != 2 {
		t.Fatalf("wide cell not preserved: %#v", wide)
	}
	if s.RowsData[0].Hash == 0 {
		t.Fatal("row hash missing")
	}
}

func TestTerminalQueryResponsesAreRetained(t *testing.T) {
	e := New(80, 24, 100)
	defer e.Close()

	e.Write([]byte("\x1b[5n"))
	responses := e.TakeResponses()
	if len(responses) == 0 {
		t.Fatal("expected terminal status response")
	}
	if !bytes.Contains(bytes.Join(responses, nil), []byte("\x1b[0n")) {
		t.Fatalf("unexpected response: %q", bytes.Join(responses, nil))
	}
	if more := e.TakeResponses(); len(more) != 0 {
		t.Fatalf("responses were not drained: %q", more)
	}
}


func TestMouseTrackingEncodesThroughVTCore(t *testing.T) {
	e := New(80, 24, 100)
	defer e.Close()

	e.Write([]byte("[?1000h[?1006h"))
	if !e.Mouse(MouseEvent{
		Col:5,Row:3,X:40,Y:60,
		Button:MouseLeft,Action:MouseDown,
	}) {
		t.Fatal("mouse event was not accepted")
	}
	data:=bytes.Join(e.TakeResponses(),nil)
	if !bytes.Contains(data,[]byte("[<0;5;3M")) {
		t.Fatalf("unexpected mouse response %q",data)
	}
}
