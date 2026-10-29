package goui

import (
	"image"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
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

// Editing keys consumed by the IME view never reach GLFW's pressed-key
// sampling, so applying them must wake a frame on its own. Without the
// invalidation the stale screen (deleted text, old caret) persists until an
// unrelated commit.
func TestFinishFrameInputInvalidatesLocalFieldEdit(t *testing.T) {
	c := NewWorkspaceClientWithConnection(nil, nil, goconfig.Default(), "")
	w := &EbitengineWindow{views: map[*WorkspaceClient]*nativeView{}}
	w.view(c)
	w.view(c).remoteFocused = true
	c.connectionMu.Lock()
	c.remoteFormVisible = true
	c.connectionMu.Unlock()
	c.remoteEditor.SetText("abc")
	c.remoteEditor.SetCaret(3, 3)

	takeTextCommands()
	noteTextCommand("deleteBackward:")
	before := w.frameRevision.Load()
	if err := w.finishFrameInput(c, false, "", nil); err != nil {
		t.Fatalf("finishFrameInput: %v", err)
	}
	if got := c.remoteEditor.Text(); got != "ab" {
		t.Fatalf("text after backspace: %q", got)
	}
	if got := w.frameRevision.Load(); got != before+1 {
		t.Fatalf("local edit did not wake a frame: before=%d after=%d", before, got)
	}

	// An empty queue must not wake a frame on its own.
	before = w.frameRevision.Load()
	if err := w.finishFrameInput(c, false, "", nil); err != nil {
		t.Fatalf("finishFrameInput: %v", err)
	}
	if got := w.frameRevision.Load(); got != before {
		t.Fatalf("empty queue changed the revision: before=%d after=%d", before, got)
	}
}
