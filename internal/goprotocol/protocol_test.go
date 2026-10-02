package goprotocol

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
)

func TestTerminalFrameRoundTrip(t *testing.T) {
	id := uuid.New()
	code := int32(7)
	cases := []TerminalEvent{
		{Kind: OutputEvent, Seq: 1, Size: TerminalSize{Columns: 80, Lines: 24}, Data: []byte{0, 1, 2, 255}},
		{Kind: ResizeEvent, Seq: 2, Size: TerminalSize{Columns: 120, Lines: 40}},
		{Kind: ExitEvent, Seq: 3, Code: &code},
	}
	for _, want := range cases {
		var buf bytes.Buffer
		if err := WriteTerminal(&buf, id, want); err != nil {
			t.Fatal(err)
		}
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if got.TerminalID != id || got.Terminal == nil {
			t.Fatalf("bad frame: %#v", got)
		}
		if got.Terminal.Kind != want.Kind || got.Terminal.Seq != want.Seq {
			t.Fatalf("got %#v want %#v", got.Terminal, want)
		}
	}
}


type shortWriter struct {
	buf bytes.Buffer
	max int
}

func (w *shortWriter) Write(p []byte)(int,error){
	if len(p)>w.max{p=p[:w.max]}
	return w.buf.Write(p)
}

func TestTerminalFrameHandlesShortWritesWithoutCopies(t *testing.T){
	id:=uuid.New()
	want:=TerminalEvent{
		Kind:OutputEvent,
		Seq:42,
		Size:TerminalSize{Columns:137,Lines:51},
		Data:bytes.Repeat([]byte("water-"),4096),
	}
	writer:=&shortWriter{max:7}
	if err:=WriteTerminal(writer,id,want);err!=nil{t.Fatal(err)}
	frame,err:=ReadFrame(&writer.buf)
	if err!=nil{t.Fatal(err)}
	if frame.TerminalID!=id || frame.Terminal==nil{t.Fatalf("bad frame: %#v",frame)}
	got:=frame.Terminal
	if got.Kind!=OutputEvent || got.Seq!=want.Seq || got.Size!=want.Size{
		t.Fatalf("metadata = %#v, want %#v",got,want)
	}
	if !bytes.Equal(got.Data,want.Data){
		t.Fatalf("output mismatch: got %d bytes want %d",len(got.Data),len(want.Data))
	}
}
