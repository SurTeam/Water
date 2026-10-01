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
