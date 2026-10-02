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


func TestOSC8URIProjectsIntoCellsAcrossChunkBoundaries(t *testing.T) {
	e:=New(20,2,100)
	defer e.Close()
	e.Write([]byte("\x1b]8;id=docs;https://example.com/a"))
	e.Write([]byte("\x1b\\link"))
	e.Write([]byte("\x1b]8;;\x1b\\ plain"))
	snap:=e.Snapshot()
	for col:=0;col<4;col++{
		cell:=snap.RowsData[0].Cells[col]
		if cell.LinkURI!="https://example.com/a"{
			t.Fatalf("cell %d URI = %q",col,cell.LinkURI)
		}
	}
	if got:=snap.RowsData[0].Cells[5].LinkURI;got!=""{
		t.Fatalf("plain cell URI = %q",got)
	}
}

func TestOSC8LinkIDsStayAlignedWhenTaggedLinkIsReused(t *testing.T) {
	e:=New(20,3,100)
	defer e.Close()
	e.Write([]byte("\x1b]8;id=same;https://example.com\x1b\\a\x1b]8;;\x1b\\"))
	e.Write([]byte("\r\n"))
	e.Write([]byte("\x1b]8;id=same;https://example.com\x1b\\b\x1b]8;;\x1b\\"))
	snap:=e.Snapshot()
	if snap.RowsData[0].Cells[0].URLID==0 || snap.RowsData[0].Cells[0].URLID!=snap.RowsData[1].Cells[0].URLID{
		t.Fatalf("tagged OSC8 link id was not reused: %d vs %d",
			snap.RowsData[0].Cells[0].URLID,snap.RowsData[1].Cells[0].URLID)
	}
	if snap.RowsData[1].Cells[0].LinkURI!="https://example.com"{
		t.Fatalf("reused link URI = %q",snap.RowsData[1].Cells[0].LinkURI)
	}
}


func TestReplaySuppressesTerminalQueryResponses(t *testing.T) {
	e:=New(80,24,100)
	defer e.Close()

	e.WriteReplay([]byte("\x1b[5n"))
	if got:=e.TakeResponses();len(got)!=0 {
		t.Fatalf("replay produced PTY responses: %q",bytes.Join(got,nil))
	}
	e.Write([]byte("\x1b[5n"))
	if got:=bytes.Join(e.TakeResponses(),nil);!bytes.Contains(got,[]byte("\x1b[0n")) {
		t.Fatalf("live query response missing: %q",got)
	}
}
