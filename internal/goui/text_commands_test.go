package goui

import (
	"image"
	"testing"
)

func TestTextCommandSpecCoversEditingKeys(t *testing.T) {
	for _, tc := range []struct{ selector, spec string }{
		{"deleteBackward:", "Backspace"},
		{"deleteForward:", "Delete"},
		{"insertNewline:", "Return"},
		{"insertTab:", "Tab"},
		{"moveLeft:", "Left"},
		{"moveRight:", "Right"},
		{"moveUp:", "Up"},
		{"moveDown:", "Down"},
		{"cancelOperation:", "Escape"},
	} {
		got, ok := textCommandSpec(tc.selector)
		if !ok || got != tc.spec {
			t.Fatalf("%s: got %q %v", tc.selector, got, ok)
		}
	}
	if _, ok := textCommandSpec("noop:"); ok {
		t.Fatal("unknown command must not reach the terminal")
	}
}

func TestMacVirtualKeySeparatesDeleteFromBackspace(t *testing.T) {
	back, ok := macVirtualKeySpec(0x33)
	if !ok || back != "Backspace" {
		t.Fatalf("backspace: %q %v", back, ok)
	}
	del, ok := macVirtualKeySpec(0x75)
	if !ok || del != "Delete" {
		t.Fatalf("forward delete: %q %v", del, ok)
	}
	if _, ok := macVirtualKeySpec(0); ok {
		t.Fatal("letter key must stay on the text path")
	}
}

func TestIMECaretFrameDividesDisplayScaleOnce(t *testing.T) {
	x, bottom, width, height, ok := imeCaretFrame(image.Rect(200, 400, 202, 432), 2)
	if !ok || x != 106 || bottom != 216 || width != 1 || height != 16 {
		t.Fatalf("got %v %v %v %v %v", x, bottom, width, height, ok)
	}
	if _, _, _, _, ok := imeCaretFrame(image.Rectangle{}, 2); ok {
		t.Fatal("empty caret must not move the candidate window")
	}
}

func TestTextCommandQueuePreservesBurstOrder(t *testing.T) {
	takeTextCommands()
	noteTextCommand("insertTab:")
	noteTextCommand("deleteForward:")
	noteTextCommand("noop:")
	noteTextCommand("deleteBackward:")
	got := takeTextCommands()
	want := []string{"Tab", "Delete", "Backspace"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v", got)
		}
	}
	if again := takeTextCommands(); again != nil {
		t.Fatalf("queue not drained: %v", again)
	}
}
