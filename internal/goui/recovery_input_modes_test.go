package goui

import (
	"bytes"
	"github.com/SurTeam/Water/internal/govt"
	"testing"
)

func TestRecoveryInputModesRestoreApplicationWheel(t *testing.T) {
	before := govt.New(80, 24, 100)
	defer before.Close()
	before.WriteReplay([]byte("\x1b[?1049h\x1b[?1003;1006h"))
	modes := recoveryInputModes(before.FrameSnapshot())
	after := govt.New(80, 24, 100)
	defer after.Close()
	after.WriteReplay([]byte(modes))
	if !after.Mouse(govt.MouseEvent{Col: 5, Row: 3, Button: govt.MouseWheel, Action: govt.MouseUp}) {
		t.Fatal("recovered application wheel fell through to scrollback")
	}
	if got := string(bytes.Join(after.TakeResponses(), nil)); got != "\x1b[<64;5;3M" {
		t.Fatalf("wheel report=%q", got)
	}
	after.WriteReplay([]byte("\x1b[?1000l"))
	if after.Mouse(govt.MouseEvent{Col: 5, Row: 3, Button: govt.MouseWheel, Action: govt.MouseUp}) {
		t.Fatal("retained application reset did not override recovery")
	}
}
